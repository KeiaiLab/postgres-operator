/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/prometheus/client_golang/prometheus/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

// 본 파일은 네트워크 trust 제거(설계 2026-09-25 §3.18, ADR-0034)를 본다 — pg_hba 는 local trust + host scram 만,
// postgres 비밀번호는 오퍼레이터가 Secret 에 맞추고(verifier 만 exec 에 싣는다), 맞추기 전엔 설정 CM·STS 를
// 바꾸지 않고, 설정 적재가 파일보다 이른 멤버만 reload 한다.

const (
	// testSuperuserPassword 는 시험 Secret 비밀번호다 — passfile 이스케이프 문자(: \)와 공백을 지난다.
	testSuperuserPassword = `plain-TEXT 7:q\z`
	// rfcUser·rfcPassword·rfcSalt 는 RFC 7677 §3 의 예다.
	rfcPassword = "pencil"
	rfcSalt     = "W22ZaJ0SNY7soEsUEjb6gQ=="
	rfcVerifier = "SCRAM-SHA-256$4096:W22ZaJ0SNY7soEsUEjb6gQ==$WG5d8oPm3OtcPnkdi4Uo7BkeZkBFzpcXkuLmtbsT4qY=:" +
		"wfPLwcE6nTWhTAmQ7tl2KeoiWGPlZqQxSrmfPwDl2dU="
	// rfcAuthMessage 는 client-first-bare , server-first , client-final-without-proof 다.
	rfcAuthMessage = "n=user,r=rOprNGfwEbeRWgbNEkqO," +
		"r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096," +
		"c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0"
	rfcClientProof     = "dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ="
	rfcServerSignature = "6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4="

	// legacyTrustHBA 는 SEC1 전 판이 렌더한 pg_hba 다(L1 전 라이브 설정 CM).
	legacyTrustHBA = "local all all trust\nhost all postgres 10.0.0.0/8 trust\nhost replication all 10.0.0.0/8 trust\n"
	// hbaFresh·hbaStale 은 판정 exec 의 답이다: 신선|trust 줄|오류 줄.
	hbaFresh = "t|0|0"
	hbaStale = "f|0|0"
)

// superuserSecret 은 basic-auth 형식 superuser Secret 이다.
func superuserSecret(name, username, password string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: runtimeTestNS},
		Type:       corev1.SecretTypeBasicAuth,
		Data:       map[string][]byte{superuserUsernameKey: []byte(username), superuserPasswordKey: []byte(password)},
	}
}

// withSuperuserRef 는 클러스터가 <name>-superuser 를 가리키게 하고 그 Secret(시험 비밀번호)을 돌려준다.
func withSuperuserRef(cluster *postgresv1alpha1.PostgresCluster) *corev1.Secret {
	name := cluster.Name + "-superuser"
	cluster.Spec.SuperuserSecretRef = &corev1.LocalObjectReference{Name: name}
	return superuserSecret(name, superuserName, testSuperuserPassword)
}

// knownVerifier 는 password 의 고정 salt verifier 다.
func knownVerifier(t *testing.T, password string) string {
	t.Helper()
	v, err := scramVerifier(password, []byte("sec1-test-salt-!"), scramIterations)
	if err != nil {
		t.Fatalf("scramVerifier: %v", err)
	}
	return v
}

// pgSim 은 멤버 postgres 흉내다 — pg_authid.rolpassword(한 벌, primary 에만 쓰인다) · pg_is_in_recovery ·
// hba 판정 · pg_reload_conf · 승격 마커에 답하고, 모든 exec 명령을 남긴다.
type pgSim struct {
	mu        sync.Mutex
	log       *writeLog
	recovery  map[string]string // 파드 → t|f (없으면 exec 실패)
	hba       map[string]string // 파드 → 판정 답 (없으면 exec 실패)
	markers   map[string]string
	stored    string // rolpassword — NULL 은 ""
	ignore    bool   // ALTER ROLE 을 받고도 바꾸지 않는다
	failAlter bool
	commands  []string
	alters    []string // ALTER ROLE 로 받은 verifier
	reloads   []string // pg_reload_conf 를 받은 파드
}

func newPGSim(log *writeLog, primary string, replicas ...string) *pgSim {
	sim := &pgSim{log: log, recovery: map[string]string{primary: pgBoolFalse}, hba: map[string]string{primary: hbaFresh},
		markers: map[string]string{primary: "1"}}
	for _, pod := range replicas {
		sim.recovery[pod], sim.hba[pod] = pgBoolTrue, hbaFresh
	}
	return sim
}

func (x *pgSim) Exec(_ context.Context, target BackupSidecarTarget, command []string) ([]byte, error) {
	joined := strings.Join(command, " ")
	x.mu.Lock()
	defer x.mu.Unlock()
	x.commands = append(x.commands, joined)
	switch {
	case strings.Contains(joined, "ALTER ROLE"):
		x.log.add("exec alter " + target.Pod)
		if x.failAlter {
			return nil, fmt.Errorf("exec %s: ERROR: permission denied", target.Pod)
		}
		_, quoted, _ := strings.Cut(joined, "PASSWORD '")
		verifier, _, _ := strings.Cut(quoted, "'")
		x.alters = append(x.alters, verifier)
		if !x.ignore {
			x.stored = verifier
		}
		return nil, nil
	case strings.Contains(joined, "rolpassword"):
		x.log.add("exec verifier " + target.Pod)
		return []byte(x.stored + "\n"), nil
	case strings.Contains(joined, "pg_hba_file_rules"):
		x.log.add("exec hba " + target.Pod)
		answer, ok := x.hba[target.Pod]
		if !ok {
			return nil, fmt.Errorf("exec %s: unreachable", target.Pod)
		}
		return []byte(answer + "\n"), nil
	case strings.Contains(joined, "pg_reload_conf"):
		x.log.add("exec reload " + target.Pod)
		x.reloads = append(x.reloads, target.Pod)
		return []byte("t\n"), nil
	case strings.Contains(joined, recoveryQuery):
		answer, ok := x.recovery[target.Pod]
		if !ok {
			return nil, fmt.Errorf("exec %s: unreachable", target.Pod)
		}
		return []byte(answer + "\n"), nil
	case strings.Contains(joined, promotedPrimaryMarker):
		return []byte(x.markers[target.Pod]), nil
	}
	return nil, nil
}

func (x *pgSim) snapshot() (alters, reloads, commands []string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return slices.Clone(x.alters), slices.Clone(x.reloads), slices.Clone(x.commands)
}

// authWorld 는 L1 직전 샤드다 — primary 0 · replica 1(runtime CM primary = 0, 둘 다 Ready), 라이브 설정 CM 은 옛 trust
// hba, 라이브 STS 는 옛 템플릿이다. secret 이 nil 을 주면 Secret 객체를 두지 않는다.
type authWorld struct {
	name    string
	cluster *postgresv1alpha1.PostgresCluster
	c       client.Client
	r       *PostgresClusterReconciler
	pg      *pgSim
	log     *writeLog
	rec     *events.FakeRecorder
	liveCM  *corev1.ConfigMap
	liveSTS *appsv1.StatefulSet
}

func newAuthWorld(t *testing.T, name string, secret func(*postgresv1alpha1.PostgresCluster) *corev1.Secret) *authWorld {
	t.Helper()
	cluster, runtimeCM := runtimeCluster(name)
	objs := []client.Object{}
	if s := secret(cluster); s != nil {
		objs = append(objs, s)
	}
	liveCM := buildConfigMap(cluster, ShardConfigMapName(name, 0), "shard", 0, nil)
	liveCM.Data["pg_hba.conf"] = legacyTrustHBA
	liveSTS := oldTemplateSTS(cluster)
	liveSTS.Status = appsv1.StatefulSetStatus{Replicas: 2, ReadyReplicas: 2} // 샤드 primary Ready — 옛 판은 매번 primary 를 reload 했다
	primary, replica := shardMember(name, 0, statusapi.RolePrimary), shardMember(name, 1, statusapi.RoleReplica)
	objs = append(objs, cluster, runtimeCM, liveCM, liveSTS, primary, replica)

	log := &writeLog{}
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(log.funcs()).Build()
	pg := newPGSim(log, primary.Name, replica.Name)
	rec := events.NewFakeRecorder(wideEventBuffer)
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: pg, Recorder: rec}
	t.Cleanup(func() { DeleteMetricsFor(runtimeTestNS, name) })
	return &authWorld{name: name, cluster: cluster, c: c, r: r, pg: pg, log: log, rec: rec, liveCM: liveCM, liveSTS: liveSTS}
}

func (w *authWorld) condition(t *testing.T) *metav1.Condition {
	t.Helper()
	var got postgresv1alpha1.PostgresCluster
	if err := w.c.Get(context.Background(), client.ObjectKeyFromObject(w.cluster), &got); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	return meta.FindStatusCondition(got.Status.Conditions, ConditionSuperuserAuthPending)
}

// configCM 은 샤드 설정 CM 데이터다.
func (w *authWorld) configCM(t *testing.T) map[string]string {
	t.Helper()
	var cm corev1.ConfigMap
	if err := w.c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: w.liveCM.Name}, &cm); err != nil {
		t.Fatalf("get config CM: %v", err)
	}
	return cm.Data
}

// hbaRows 는 pg_hba.conf 규칙 줄의 열이다(주석·빈 줄 제외).
func hbaRows(conf string) [][]string {
	var rows [][]string
	for line := range strings.SplitSeq(conf, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		rows = append(rows, fields)
	}
	return rows
}

// TestHBANoNetworkTrust 는 렌더한 pg_hba 에 비-local trust 가 없고(TLS on/off × 대역 비움/지정), 클라이언트 줄은
// host(ssl) all all <대역> scram, replication 줄은 host replication postgres <대역> scram 인지 본다. 대역은 CR
// spec.postgresql.hostAuthCIDRs 를 표준 표기로 쓰고, 비면 사설 대역 셋이다.
func TestHBANoNetworkTrust(t *testing.T) {
	t.Parallel()
	private := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
	cases := []struct {
		name  string
		tls   bool
		cidrs []string
		want  []string
	}{
		{name: "plain default", want: private},
		{name: "tls default", tls: true, want: private},
		{name: "plain pod cidr", cidrs: []string{"10.42.0.0/16"}, want: []string{"10.42.0.0/16"}},
		{name: "tls normalized", tls: true, cidrs: []string{" 10.42.7.9/16", "fd00:10::5/64"}, want: []string{"10.42.0.0/16", "fd00:10::/64"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			conf := renderPGHBAConf(tc.tls, tc.cidrs)
			rows := hbaRows(conf)

			hostType := "host"
			if tc.tls {
				hostType = "hostssl"
			}
			want := make([][]string, 0, 1+2*len(tc.want))
			want = append(want, []string{"local", "all", "all", "trust"})
			for _, cidr := range tc.want {
				want = append(want, []string{hostType, "all", "all", cidr, "scram-sha-256"})
			}
			for _, cidr := range tc.want {
				want = append(want, []string{"host", "replication", "postgres", cidr, "scram-sha-256"})
			}
			for _, row := range rows {
				if row[0] != "local" && row[len(row)-1] == "trust" {
					t.Fatalf("network trust rule %q in pg_hba:\n%s", row, conf)
				}
			}
			if !equality.Semantic.DeepEqual(rows, want) {
				t.Fatalf("pg_hba rules = %q, want %q", rows, want)
			}
		})
	}

	// CR 의 대역이 설정 CM 에 그대로 간다.
	cluster := haCluster("hba-cr", postgresv1alpha1.ShardingModeNone)
	cluster.Spec.PostgreSQL = &postgresv1alpha1.PostgreSQLSpec{HostAuthCIDRs: []string{"10.42.0.0/16"}}
	cm := buildConfigMap(cluster, "hba-cr-cm", "shard", 0, nil)
	if got, want := cm.Data["pg_hba.conf"], renderPGHBAConf(false, []string{"10.42.0.0/16"}); got != want {
		t.Fatalf("config CM pg_hba = %q, want the CR hostAuthCIDRs render %q", got, want)
	}
}

// TestScramRFC7677 은 verifier 수학을 RFC 7677 §3 의 예(user/pencil)로 본다 — 서버 쪽 검증(ClientProof 로 되찾은
// ClientKey 의 해시 = StoredKey)과 ServerSignature 가 그 교환과 맞고, 대조는 같은 비밀번호만 참이다.
func TestScramRFC7677(t *testing.T) {
	t.Parallel()
	salt, err := base64.StdEncoding.DecodeString(rfcSalt)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := scramVerifier(rfcPassword, salt, scramIterations)
	if err != nil {
		t.Fatalf("scramVerifier: %v", err)
	}
	if verifier != rfcVerifier {
		t.Fatalf("verifier = %q, want %q", verifier, rfcVerifier)
	}

	// 서버가 하는 검증을 이 verifier 의 두 키로 그대로 한다.
	keys := strings.Split(strings.SplitN(verifier, "$", 3)[2], ":")
	storedKey, _ := base64.StdEncoding.DecodeString(keys[0])
	serverKey, _ := base64.StdEncoding.DecodeString(keys[1])
	proof, _ := base64.StdEncoding.DecodeString(rfcClientProof)
	signature := hmacSHA256(storedKey, rfcAuthMessage)
	clientKey := make([]byte, len(proof))
	for i := range proof {
		clientKey[i] = proof[i] ^ signature[i]
	}
	if sum := sha256.Sum256(clientKey); !hmac.Equal(sum[:], storedKey) {
		t.Fatal("StoredKey does not verify the RFC 7677 client proof")
	}
	if got := base64.StdEncoding.EncodeToString(hmacSHA256(serverKey, rfcAuthMessage)); got != rfcServerSignature {
		t.Fatalf("server signature = %q, want %q", got, rfcServerSignature)
	}

	for label, tc := range map[string]struct {
		stored, password string
		want             bool
		scram            bool
	}{
		"same":            {stored: rfcVerifier, password: rfcPassword, want: true, scram: true},
		"other password":  {stored: rfcVerifier, password: "pencil2", scram: true},
		"server key only": {stored: strings.Replace(rfcVerifier, keys[0], keys[1], 1), password: rfcPassword, scram: true},
		"stored key only": {stored: strings.Replace(rfcVerifier, keys[1], keys[0], 1), password: rfcPassword, scram: true},
		"other mechanism": {stored: strings.Replace(rfcVerifier, scramMechanism, "SCRAM-SHA-1", 1), password: rfcPassword},
		"null":            {stored: "", password: rfcPassword},
		"md5":             {stored: "md5c2f3e8c7d8ab1a9a7a1d5b7b4c3b2a1f", password: rfcPassword},
	} {
		got, err := verifierMatches(tc.stored, tc.password)
		if got != tc.want || (err == nil) != tc.scram {
			t.Errorf("%s: verifierMatches = %v, err %v; want %v (SCRAM %v)", label, got, err, tc.want, tc.scram)
		}
	}
}

// TestSuperuserAuthVerifier 는 ensureSuperuserAuth 가 rolpassword 를 Secret 비밀번호와 대조하는지 본다 — NULL·다름은
// 새 salt verifier(평문 아님)로 ALTER ROLE 한 번 뒤 다시 읽어 대조 · 같으면 ALTER 0 · ALTER 가 안 먹거나 실패하거나
// 실제 primary 가 없으면 미룬다(SuperuserAuthUnverified).
func TestSuperuserAuthVerifier(t *testing.T) {
	t.Parallel()
	const read, alter = "verifier", "alter"
	cases := []struct {
		name       string
		stored     string // NULL · same(Secret 비밀번호) · other
		sim        func(*pgSim)
		wantExecs  []string
		wantReady  bool
		wantReason string
	}{
		{name: "null", wantExecs: []string{read, alter, read}, wantReady: true},
		{name: "same", stored: "same", wantExecs: []string{read}, wantReady: true},
		{name: "other", stored: "other", wantExecs: []string{read, alter, read}, wantReady: true},
		{name: "alter ignored", sim: func(x *pgSim) { x.ignore = true },
			wantExecs: []string{read, alter, read}, wantReason: ReasonSuperuserAuthUnverified},
		{name: "alter fails", sim: func(x *pgSim) { x.failAlter = true },
			wantExecs: []string{read, alter}, wantReason: ReasonSuperuserAuthUnverified},
		{name: "no real primary", sim: func(x *pgSim) { x.recovery = map[string]string{} },
			wantReason: ReasonSuperuserAuthUnverified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newAuthWorld(t, "auth-"+strings.ReplaceAll(tc.name, " ", "-"), withSuperuserRef)
			switch tc.stored {
			case "same":
				w.pg.stored = knownVerifier(t, testSuperuserPassword)
			case "other":
				w.pg.stored = knownVerifier(t, "old-password")
			}
			if tc.sim != nil {
				tc.sim(w.pg)
			}

			got, err := w.r.ensureSuperuserAuth(context.Background(), w.cluster, ordinalShardRef(w.cluster, 0))
			if err != nil {
				t.Fatalf("ensureSuperuserAuth: %v", err)
			}
			if got.ready != tc.wantReady || got.reason != tc.wantReason {
				t.Fatalf("auth = %+v, want ready %v reason %q", got, tc.wantReady, tc.wantReason)
			}
			var execs []string
			for _, line := range w.log.lines {
				if kind, ok := strings.CutPrefix(line, "exec "); ok && (strings.HasPrefix(kind, read) || strings.HasPrefix(kind, alter)) {
					execs = append(execs, strings.Fields(kind)[0])
				}
			}
			if !slices.Equal(execs, tc.wantExecs) {
				t.Fatalf("verifier execs = %q, want %q", execs, tc.wantExecs)
			}
			alters, _, _ := w.pg.snapshot()
			for _, v := range alters {
				same, err := verifierMatches(v, testSuperuserPassword)
				if err != nil || !same || !strings.HasPrefix(v, fmt.Sprintf("SCRAM-SHA-256$%d:", scramIterations)) {
					t.Fatalf("ALTER ROLE carried %q, want a SCRAM-SHA-256 verifier (4096) of the Secret password", v)
				}
				if v == knownVerifier(t, testSuperuserPassword) {
					t.Fatal("ALTER ROLE reused a known salt, want a fresh one")
				}
			}
		})
	}
}

// TestSuperuserAuthTarget 은 verifier 를 싣는 곳과 salt 를 본다 — 실제 primary 둘이면 런타임 CM primary 에 싣고,
// ALTER 마다 새 16 바이트 salt 다.
func TestSuperuserAuthTarget(t *testing.T) {
	t.Parallel()
	const name = "auth-target"
	w := newAuthWorld(t, name, withSuperuserRef)
	rogue, cmPrimary := graphMember(name, 0), graphMember(name, 1)

	// 둘 다 primary 를 보고하고 pg_is_in_recovery=f 다 — 런타임 CM 은 1 을 가리킨다.
	var pod corev1.Pod
	if err := w.c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: cmPrimary}, &pod); err != nil {
		t.Fatal(err)
	}
	setMemberRole(&pod, ShardServiceName(name, 0), statusapi.RolePrimary, time.Now())
	if err := w.c.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	cm, _, err := w.r.runtimeConfigMap(context.Background(), w.cluster, "shard-0")
	if err != nil {
		t.Fatal(err)
	}
	cm.Data[runtimeKeyPrimaryEndpoint] = podDNS(name, cmPrimary)
	if err := w.c.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	w.pg.recovery[rogue], w.pg.recovery[cmPrimary] = pgBoolFalse, pgBoolFalse
	w.pg.ignore = true

	for range 2 {
		if _, err := w.r.ensureSuperuserAuth(context.Background(), w.cluster, ordinalShardRef(w.cluster, 0)); err != nil {
			t.Fatalf("ensureSuperuserAuth: %v", err)
		}
	}

	if n, m := w.log.count("exec alter "+cmPrimary), w.log.count("exec alter "); n != 2 || m != 2 {
		t.Fatalf("ALTER on the runtime ConfigMap primary = %d of %d, want 2 of 2: %q", n, m, w.log.lines)
	}
	alters, _, _ := w.pg.snapshot()
	salts := map[string]bool{}
	for _, v := range alters {
		params := strings.SplitN(v, "$", 3)[1]
		raw, err := base64.StdEncoding.DecodeString(strings.SplitN(params, ":", 2)[1])
		if err != nil || len(raw) != scramSaltLen {
			t.Fatalf("salt %q: %d bytes (err %v), want %d", params, len(raw), err, scramSaltLen)
		}
		salts[string(raw)] = true
	}
	if len(salts) != len(alters) {
		t.Fatalf("salts repeat across ALTER ROLE: %d distinct of %d", len(salts), len(alters))
	}
}

// TestSuperuserAuthGate 는 Reconcile 이 superuser 인증을 확인하기 전엔 라이브 설정 CM·STS 를 바꾸지 않는지 본다 —
// Secret 이 없거나(참조 없음 포함) username 이 postgres 가 아니거나 password 가 비었거나 인쇄 가능 ASCII 가 아니면
// SuperuserAuthPending=True(사유별) · 설정 CM 갱신 0 · STS 쓰기 0. Secret 이 맞으면 ALTER ROLE 이 설정 CM·STS 보다
// 먼저이고 Pending=False 다.
func TestSuperuserAuthGate(t *testing.T) {
	t.Parallel()
	invalid := []struct {
		name   string
		secret func(*postgresv1alpha1.PostgresCluster) *corev1.Secret
		reason string
	}{
		{name: "ref unset", secret: func(*postgresv1alpha1.PostgresCluster) *corev1.Secret { return nil }, reason: ReasonSuperuserSecretUnset},
		{name: "secret missing", secret: func(c *postgresv1alpha1.PostgresCluster) *corev1.Secret {
			withSuperuserRef(c)
			return nil
		}, reason: ReasonSuperuserSecretMissing},
		{name: "username", secret: func(c *postgresv1alpha1.PostgresCluster) *corev1.Secret {
			s := withSuperuserRef(c)
			s.Data[superuserUsernameKey] = []byte("admin")
			return s
		}, reason: ReasonSuperuserSecretInvalid},
		{name: "empty password", secret: func(c *postgresv1alpha1.PostgresCluster) *corev1.Secret {
			s := withSuperuserRef(c)
			s.Data[superuserPasswordKey] = nil
			return s
		}, reason: ReasonSuperuserSecretInvalid},
		{name: "non ascii", secret: func(c *postgresv1alpha1.PostgresCluster) *corev1.Secret {
			s := withSuperuserRef(c)
			s.Data[superuserPasswordKey] = []byte("pässword")
			return s
		}, reason: ReasonSuperuserSecretInvalid},
		{name: "newline", secret: func(c *postgresv1alpha1.PostgresCluster) *corev1.Secret {
			s := withSuperuserRef(c)
			s.Data[superuserPasswordKey] = []byte("password\n")
			return s
		}, reason: ReasonSuperuserSecretInvalid},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newAuthWorld(t, "gate-"+strings.ReplaceAll(tc.name, " ", "-"), tc.secret)

			reconcileCluster(t, w.r, w.cluster)

			if n := w.log.count("exec alter ") + w.log.count("exec verifier "); n != 0 {
				t.Fatalf("verifier exec = %d, want 0 with an unusable Secret: %q", n, w.log.lines)
			}
			if n := w.log.count("update cm/"+w.liveCM.Name) + w.log.count("patch cm/"+w.liveCM.Name); n != 0 {
				t.Fatalf("config CM writes = %d, want 0: %q", n, w.log.lines)
			}
			if got := w.configCM(t); got["pg_hba.conf"] != legacyTrustHBA {
				t.Fatalf("live pg_hba changed to %q before the password was verified", got["pg_hba.conf"])
			}
			if n := w.log.count("update sts/") + w.log.count("patch sts/") + w.log.count("create sts/"); n != 0 {
				t.Fatalf("STS writes = %d, want 0 (live template kept): %q", n, w.log.lines)
			}
			if got := stsTemplate(t, w.c, w.liveSTS.Name); !equality.Semantic.DeepEqual(got, w.liveSTS.Spec.Template) {
				t.Fatalf("live template changed:\nwant=%s\ngot=%s", envDump(w.liveSTS.Spec.Template), envDump(got))
			}
			cond := w.condition(t)
			if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != tc.reason {
				t.Fatalf("SuperuserAuthPending = %+v, want True/%s", cond, tc.reason)
			}
		})
	}

	// 확인 전이라도 없는 설정 CM·STS 는 만든다 — 지킬 라이브 객체가 없다(멤버 파드만 남은 샤드).
	t.Run("absent created", func(t *testing.T) {
		t.Parallel()
		w := newAuthWorld(t, "gate-absent", func(*postgresv1alpha1.PostgresCluster) *corev1.Secret { return nil })
		for _, obj := range []client.Object{w.liveCM, w.liveSTS} {
			if err := w.c.Delete(context.Background(), obj); err != nil {
				t.Fatal(err)
			}
		}

		reconcileCluster(t, w.r, w.cluster)

		if w.log.first("create cm/"+w.liveCM.Name) < 0 || w.log.first("create sts/"+w.liveSTS.Name) < 0 {
			t.Fatalf("absent config CM and StatefulSet not created: %q", w.log.lines)
		}
		if got := w.configCM(t)["pg_hba.conf"]; got != renderPGHBAConf(false, nil) {
			t.Fatalf("created config CM pg_hba = %q, want the no-trust render", got)
		}
		cond := w.condition(t)
		if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != ReasonSuperuserSecretUnset {
			t.Fatalf("SuperuserAuthPending = %+v, want True/%s", cond, ReasonSuperuserSecretUnset)
		}
	})

	t.Run("verified first", func(t *testing.T) {
		t.Parallel()
		w := newAuthWorld(t, "gate-verified", withSuperuserRef)

		reconcileCluster(t, w.r, w.cluster)

		alter, cm, sts := w.log.first("exec alter "), w.log.first("update cm/"+w.liveCM.Name), w.log.first("update sts/"+w.liveSTS.Name)
		if alter < 0 || cm < 0 || sts < 0 || alter > cm || alter > sts {
			t.Fatalf("order = alter %d · config CM %d · STS %d, want the password set before config and template: %q",
				alter, cm, sts, w.log.lines)
		}
		if got := w.configCM(t)["pg_hba.conf"]; got != renderPGHBAConf(false, nil) {
			t.Fatalf("config CM pg_hba = %q, want the no-trust render", got)
		}
		cond := w.condition(t)
		if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonSuperuserAuthVerified {
			t.Fatalf("SuperuserAuthPending = %+v, want False/%s", cond, ReasonSuperuserAuthVerified)
		}
		if n := strings.Count(strings.Join(drainEvents(w.rec), "\n"), "Normal "+superuserAuthUpdatedReason+" "); n != 1 {
			t.Fatalf("%s events = %d, want 1", superuserAuthUpdatedReason, n)
		}

		// 다음 reconcile 은 대조만 한다 — ALTER 0.
		reconcileCluster(t, w.r, w.cluster)
		if n := w.log.count("exec alter "); n != 1 {
			t.Fatalf("ALTER ROLE count after the second reconcile = %d, want 1", n)
		}
	})
}

// TestSuperuserAuthNoMembers 는 멤버 파드가 없는 샤드는 대조할 곳이 없어 막지 않는지 본다 — 새 클러스터는 설정 CM·STS
// 를 만들고, 라이브 객체도 바꾼다. Secret 문제는 condition 으로만 알린다(첫 primary 가 뜨면 맞춘다).
func TestSuperuserAuthNoMembers(t *testing.T) {
	t.Parallel()
	t.Run("fresh cluster", func(t *testing.T) {
		t.Parallel()
		cluster := haCluster("gate-fresh", postgresv1alpha1.ShardingModeNone)
		scheme := runtimeScheme(t)
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).
			WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).Build()
		r := &PostgresClusterReconciler{Client: c, Scheme: scheme}
		t.Cleanup(func() { DeleteMetricsFor(runtimeTestNS, cluster.Name) })

		reconcileCluster(t, r, cluster)

		tpl := stsTemplate(t, c, ShardStatefulSetName(cluster.Name, 0))
		if envByNameOf(tpl.Spec.Containers[0])[pgPassFileEnv].Value != primaryPGPassFile {
			t.Fatalf("fresh STS lacks %s: %s", pgPassFileEnv, envDump(tpl))
		}
		var got postgresv1alpha1.PostgresCluster
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(cluster), &got); err != nil {
			t.Fatal(err)
		}
		cond := meta.FindStatusCondition(got.Status.Conditions, ConditionSuperuserAuthPending)
		if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != ReasonSuperuserSecretUnset {
			t.Fatalf("SuperuserAuthPending = %+v, want True/%s", cond, ReasonSuperuserSecretUnset)
		}
	})

	// 라이브 설정 CM·STS 가 있어도 멤버 파드가 없으면 바꾼다.
	t.Run("no members", func(t *testing.T) {
		t.Parallel()
		w := newAuthWorld(t, "gate-no-members", func(*postgresv1alpha1.PostgresCluster) *corev1.Secret { return nil })
		for _, pod := range []string{graphMember(w.name, 0), graphMember(w.name, 1)} {
			if err := w.c.Delete(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: runtimeTestNS, Name: pod}}); err != nil {
				t.Fatal(err)
			}
		}

		reconcileCluster(t, w.r, w.cluster)

		if w.log.first("update cm/"+w.liveCM.Name) < 0 || w.log.first("update sts/"+w.liveSTS.Name) < 0 {
			t.Fatalf("a shard without members kept its live config and template: %q", w.log.lines)
		}
		cond := w.condition(t)
		if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != ReasonSuperuserSecretUnset {
			t.Fatalf("SuperuserAuthPending = %+v, want True/%s", cond, ReasonSuperuserSecretUnset)
		}
	})
}

// TestSuperuserAuthExempt 는 인증 대조가 막지 않는 것을 본다 — 복제 클러스터(대상 밖, verifier exec 0 · NotApplicable)와
// 멤버를 0 으로 줄이는 반영(하이버네이션).
func TestSuperuserAuthExempt(t *testing.T) {
	t.Parallel()
	t.Run("replica cluster", func(t *testing.T) {
		t.Parallel()
		w := newAuthWorld(t, "gate-replica", func(c *postgresv1alpha1.PostgresCluster) *corev1.Secret {
			c.Spec.ExternalClusters = []postgresv1alpha1.ExternalClusterSpec{{
				Name: "src", ConnectionParameters: map[string]string{"host": "src-rw.data.svc"},
			}}
			c.Spec.Replica = &postgresv1alpha1.ReplicaClusterSpec{Enabled: true, Source: "src"}
			c.Spec.Bootstrap = &postgresv1alpha1.BootstrapSpec{PgBaseBackup: &postgresv1alpha1.PgBaseBackupBootstrapSpec{Source: "src"}}
			return nil
		})

		reconcileCluster(t, w.r, w.cluster)

		if n := w.log.count("exec verifier ") + w.log.count("exec alter "); n != 0 {
			t.Fatalf("verifier exec on a replica cluster = %d, want 0", n)
		}
		if w.log.first("update sts/"+w.liveSTS.Name) < 0 {
			t.Fatalf("replica cluster template not applied: %q", w.log.lines)
		}
		cond := w.condition(t)
		if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonNotApplicable {
			t.Fatalf("SuperuserAuthPending = %+v, want False/%s", cond, ReasonNotApplicable)
		}
	})

	t.Run("hibernate", func(t *testing.T) {
		t.Parallel()
		w := newAuthWorld(t, "gate-hibernate", func(c *postgresv1alpha1.PostgresCluster) *corev1.Secret {
			c.Annotations[AnnotationHibernation] = "on"
			return nil
		})

		reconcileCluster(t, w.r, w.cluster)

		var sts appsv1.StatefulSet
		if err := w.c.Get(context.Background(), client.ObjectKeyFromObject(w.liveSTS), &sts); err != nil {
			t.Fatal(err)
		}
		if sts.Spec.Replicas == nil || *sts.Spec.Replicas != 0 {
			t.Fatalf("hibernated STS replicas = %v, want 0 even with the password unverified", sts.Spec.Replicas)
		}
	})
}

// TestSuperuserAuthNamed 는 named 샤드(승격된 리샤드 타깃)도 확인 전 라이브 설정 CM·STS 를 바꾸지 않는지 본다.
func TestSuperuserAuthNamed(t *testing.T) {
	t.Parallel()
	const name = "gate-named"
	cluster := haCluster(name, postgresv1alpha1.ShardingModeNative)
	sts, svc := TargetShardStatefulSetName(name, "t1"), TargetShardServiceName(name, "t1")
	labels := ReshardTargetSelectorLabels(name, "t1")
	liveCM := buildTargetShardConfigMap(cluster, "t1", nil)
	liveCM.Data["pg_hba.conf"] = legacyTrustHBA
	liveSTS := buildTargetShardStatefulSetWithMembers(cluster, "t1", "example.com/postgres:18", "18", 2, "",
		cluster.Spec.Shards.Storage, corev1.ResourceRequirements{}, liveCM.Name, "old-hash")
	runtime := buildRuntimeConfigMap(cluster, "t1", shardRuntime{
		PrimaryEndpoint: defaultEndpoint(sts+"-0", svc, runtimeTestNS), PromotionEpoch: 1, ClusterInitialized: true,
	})
	shardRange := &postgresv1alpha1.ShardRange{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-sr", Namespace: runtimeTestNS},
		Spec: postgresv1alpha1.ShardRangeSpec{
			Cluster: name, Keyspace: "default",
			Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "t1"}},
		},
	}
	log := &writeLog{}
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, shardRange, liveCM, liveSTS, runtime,
			memberPod(sts+"-0", svc, labels, statusapi.RolePrimary), memberPod(sts+"-1", svc, labels, statusapi.RoleReplica)).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(log.funcs()).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: newPGSim(log, sts+"-0", sts+"-1")}
	t.Cleanup(func() { DeleteMetricsFor(runtimeTestNS, name) })

	reconcileCluster(t, r, cluster)

	if n := log.count("update cm/"+liveCM.Name) + log.count("update sts/"+sts); n != 0 {
		t.Fatalf("named shard writes = %d, want 0 before the password is verified: %q", n, log.lines)
	}
	var got postgresv1alpha1.PostgresCluster
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(cluster), &got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, ConditionSuperuserAuthPending)
	if cond == nil || cond.Status != metav1.ConditionTrue || !strings.Contains(cond.Message, "t1: ") {
		t.Fatalf("SuperuserAuthPending = %+v, want True naming shard t1", cond)
	}
}

// TestSuperuserNoPlaintext 는 Secret 비밀번호 평문이 exec 명령·이벤트·condition·로그 어디에도 없는지 본다 — ALTER 가
// 도는 길(NULL · 다름 · 안 먹음 · 실패) 모두. verifier 도 이벤트·condition·로그에 없다.
func TestSuperuserNoPlaintext(t *testing.T) {
	t.Parallel()
	for label, setup := range map[string]func(w *authWorld){
		"null":          func(*authWorld) {},
		"other":         func(w *authWorld) { w.pg.stored = knownVerifier(t, "old-password") },
		"alter ignored": func(w *authWorld) { w.pg.ignore = true },
		"alter fails":   func(w *authWorld) { w.pg.failAlter = true },
	} {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			w := newAuthWorld(t, "plain-"+strings.ReplaceAll(label, " ", "-"), withSuperuserRef)
			setup(w)
			var logs strings.Builder
			var logMu sync.Mutex
			ctx := crlog.IntoContext(context.Background(), funcr.New(func(prefix, args string) {
				logMu.Lock()
				defer logMu.Unlock()
				logs.WriteString(prefix + " " + args + "\n")
			}, funcr.Options{Verbosity: 10}))

			for range 2 {
				if _, err := w.r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(w.cluster)}); err != nil {
					t.Fatalf("Reconcile: %v", err)
				}
			}

			alters, _, commands := w.pg.snapshot()
			if label != "alter fails" && len(alters) == 0 {
				t.Fatalf("no ALTER ROLE ran; the plaintext search would be vacuous: %q", w.log.lines)
			}
			var got postgresv1alpha1.PostgresCluster
			if err := w.c.Get(context.Background(), client.ObjectKeyFromObject(w.cluster), &got); err != nil {
				t.Fatal(err)
			}
			conds := make([]string, 0, len(got.Status.Conditions))
			for _, cond := range got.Status.Conditions {
				conds = append(conds, cond.Message)
			}
			logMu.Lock()
			logged := logs.String()
			logMu.Unlock()
			surfaces := map[string]string{
				"exec":      strings.Join(commands, "\n"),
				"event":     strings.Join(drainEvents(w.rec), "\n"),
				"condition": strings.Join(conds, "\n"),
				"log":       logged,
			}
			for surface, text := range surfaces {
				if strings.Contains(text, testSuperuserPassword) {
					t.Errorf("plaintext password in %s:\n%s", surface, text)
				}
				if surface == "exec" {
					continue
				}
				for _, v := range alters {
					if strings.Contains(text, v) {
						t.Errorf("verifier in %s", surface)
					}
				}
			}
		})
	}
}

// TestSuperuserAuthEnv 는 템플릿이 superuser 자격을 싣는지 본다 — init 은 LOCAL_SUPERUSER_PASSWORD =
// secretKeyRef{superuserSecretRef, password, optional false} 와 PGPASSFILE, postgres 컨테이너는 PGPASSFILE.
// 복제 클러스터는 둘 다 없다(원천 자격은 PRIMARY_PASSWORD).
func TestSuperuserAuthEnv(t *testing.T) {
	t.Parallel()
	build := func(cluster *postgresv1alpha1.PostgresCluster) *appsv1.StatefulSet {
		return buildPGStatefulSet(cluster, ShardStatefulSetName(cluster.Name, 0), ShardServiceName(cluster.Name, 0), 0,
			"example.com/postgres:18", ShardConfigMapName(cluster.Name, 0), "18", 2,
			cluster.Spec.Shards.Storage, corev1.ResourceRequirements{}, ShardRuntimeConfigMapName(cluster.Name, "shard-0"), "hash", "")
	}

	cluster := haCluster("auth-env", postgresv1alpha1.ShardingModeNone)
	withSuperuserRef(cluster)
	sts := build(cluster)
	initEnv := envByNameOf(sts.Spec.Template.Spec.InitContainers[0])
	password := initEnv[superuserPasswordEnv]
	ref := (*corev1.SecretKeySelector)(nil)
	if password.ValueFrom != nil {
		ref = password.ValueFrom.SecretKeyRef
	}
	if password.Value != "" || ref == nil || ref.Name != cluster.Spec.SuperuserSecretRef.Name ||
		ref.Key != superuserPasswordKey || ref.Optional == nil || *ref.Optional {
		t.Fatalf("init %s = %+v, want secretKeyRef{%s, %s, optional false}", superuserPasswordEnv, password,
			cluster.Spec.SuperuserSecretRef.Name, superuserPasswordKey)
	}
	for _, ctr := range []corev1.Container{sts.Spec.Template.Spec.InitContainers[0], sts.Spec.Template.Spec.Containers[0]} {
		if got := envByNameOf(ctr)[pgPassFileEnv]; got.Value != primaryPGPassFile {
			t.Fatalf("%s %s = %+v, want %s", ctr.Name, pgPassFileEnv, got, primaryPGPassFile)
		}
	}
	if _, ok := envByNameOf(sts.Spec.Template.Spec.Containers[0])[superuserPasswordEnv]; ok {
		t.Fatalf("postgres container must not carry %s", superuserPasswordEnv)
	}

	replica := haCluster("auth-env-replica", postgresv1alpha1.ShardingModeNone)
	withSuperuserRef(replica)
	replica.Spec.ExternalClusters = []postgresv1alpha1.ExternalClusterSpec{{
		Name: "src", ConnectionParameters: map[string]string{"host": "src-rw.data.svc"},
	}}
	replica.Spec.Replica = &postgresv1alpha1.ReplicaClusterSpec{Enabled: true, Source: "src"}
	replica.Spec.Bootstrap = &postgresv1alpha1.BootstrapSpec{PgBaseBackup: &postgresv1alpha1.PgBaseBackupBootstrapSpec{Source: "src"}}
	if cfg, err := replicaBootstrapConfigForCluster(replica); cfg == nil || err != nil {
		t.Fatalf("replica fixture is not a replica cluster: %v", err)
	}
	rsts := build(replica)
	for _, ctr := range []corev1.Container{rsts.Spec.Template.Spec.InitContainers[0], rsts.Spec.Template.Spec.Containers[0]} {
		env := envByNameOf(ctr)
		if _, ok := env[superuserPasswordEnv]; ok {
			t.Fatalf("replica cluster %s carries %s", ctr.Name, superuserPasswordEnv)
		}
		if _, ok := env[pgPassFileEnv]; ok {
			t.Fatalf("replica cluster %s carries %s", ctr.Name, pgPassFileEnv)
		}
	}
}

// TestHBAReloadStale 는 reloadStaleMembers 가 적재가 파일보다 이른 멤버(스탠바이 포함)에만 pg_reload_conf 를 보내고,
// 게이지가 신선하면 trust 줄 수, 낡았거나 파일 오류·판정 실패면 −1 인지 본다. 돌지 않는 멤버는 exec 0 · 시계열 0.
func TestHBAReloadStale(t *testing.T) {
	t.Parallel()
	const name = "hba-reload"
	w := newAuthWorld(t, name, withSuperuserRef)
	primary, standby := graphMember(name, 0), graphMember(name, 1)
	w.pg.hba[primary] = "t|2|0"
	w.pg.hba[standby] = hbaStale
	broken, pending := *shardMember(name, 2, statusapi.RoleReplica), *shardMember(name, 3, statusapi.RoleReplica)
	w.pg.hba[broken.Name] = "t|0|1"
	pending.Status.Phase = corev1.PodPending
	pods := []corev1.Pod{*shardMember(name, 0, statusapi.RolePrimary), *shardMember(name, 1, statusapi.RoleReplica), broken, pending}

	if err := w.r.reloadStaleMembers(context.Background(), w.cluster, pods); err != nil {
		t.Fatalf("reloadStaleMembers: %v", err)
	}

	_, reloads, _ := w.pg.snapshot()
	if !slices.Equal(reloads, []string{standby}) {
		t.Fatalf("reloaded = %q, want only the stale standby %s", reloads, standby)
	}
	gauge := func(pod string) float64 {
		return testutil.ToFloat64(MetricHBANetworkTrustRules.WithLabelValues(runtimeTestNS, name, pod))
	}
	for pod, want := range map[string]float64{primary: 2, standby: hbaRulesUnknown, broken.Name: hbaRulesUnknown} {
		if got := gauge(pod); got != want {
			t.Errorf("hba_network_trust_rules{pod=%s} = %v, want %v", pod, got, want)
		}
	}
	if w.log.count("exec hba "+pending.Name) != 0 {
		t.Fatal("judged a member that is not running")
	}
	if MetricHBANetworkTrustRules.DeleteLabelValues(runtimeTestNS, name, pending.Name) {
		t.Fatal("a member that is not running has an hba series")
	}

	// 판정 exec 가 실패한 멤버는 모름(−1)이다.
	delete(w.pg.hba, primary)
	if err := w.r.reloadStaleMembers(context.Background(), w.cluster, pods[:1]); err != nil {
		t.Fatalf("reloadStaleMembers: %v", err)
	}
	if got := gauge(primary); got != hbaRulesUnknown {
		t.Fatalf("hba_network_trust_rules after a failed check = %v, want %v", got, hbaRulesUnknown)
	}
	if MetricHBANetworkTrustRules.DeleteLabelValues(runtimeTestNS, name, standby) {
		t.Fatal("a member no longer judged kept its hba series")
	}
}

// TestHBAReloadReplaces 는 Reconcile 의 reload 가 primary 전용 매번 reload 가 아니라 적재가 낡은 멤버 reload 하나로
// 바뀌었는지 본다 — 전부 신선하면 reload 0, 스탠바이만 낡으면 그 스탠바이만. 게이지가 멤버마다 선다.
func TestHBAReloadReplaces(t *testing.T) {
	t.Parallel()
	const name = "hba-reconcile"
	w := newAuthWorld(t, name, withSuperuserRef)
	// 라우터 파드는 같은 집계 셀렉터를 가져도 postgres 가 아니다.
	router := memberPod(name+"-router-0", RouterServiceName(name), SelectorLabels(name, "router", -1), statusapi.RoleReplica)
	if err := w.c.Create(context.Background(), router); err != nil {
		t.Fatal(err)
	}

	reconcileCluster(t, w.r, w.cluster)
	if n := w.log.count("exec hba " + router.Name); n != 0 {
		t.Fatalf("judged the router pod: %q", w.log.lines)
	}
	if n := w.log.count("exec reload "); n != 0 {
		t.Fatalf("reloads with every member fresh = %d, want 0: %q", n, w.log.lines)
	}

	standby := graphMember(name, 1)
	w.pg.mu.Lock()
	w.pg.hba[standby] = hbaStale
	w.pg.mu.Unlock()
	reconcileCluster(t, w.r, w.cluster)

	_, reloads, _ := w.pg.snapshot()
	if !slices.Equal(reloads, []string{standby}) {
		t.Fatalf("reloaded = %q, want only the stale standby %s", reloads, standby)
	}
	for pod, want := range map[string]float64{graphMember(name, 0): 0, standby: hbaRulesUnknown} {
		if got := testutil.ToFloat64(MetricHBANetworkTrustRules.WithLabelValues(runtimeTestNS, name, pod)); got != want {
			t.Errorf("hba_network_trust_rules{pod=%s} = %v, want %v", pod, got, want)
		}
	}
}
