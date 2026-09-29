/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// 본 파일은 postgres superuser 비밀번호를 CR 의 Secret 에 맞춘다(설계 §3.18, ADR-0034). pg_hba 가 네트워크
// trust 를 버리면 복제·pg_rewind·pg_basebackup 은 이 비밀번호(passfile)로 scram 인증한다.
//
//	Secret(username=postgres, password) ─▶ 실제 primary 의 pg_authid.rolpassword
//	   그 salt·iteration 으로 다시 계산해 StoredKey·ServerKey 가 같으면 끝
//	   다르거나 NULL 이면 새 salt verifier 로 ALTER ROLE → 재대조
//	   참이 될 때까지 그 샤드의 설정 CM·STS 는 라이브 그대로(SuperuserAuthPending)
//
// 평문은 오퍼레이터 밖으로 나가지 않는다 — exec 에는 verifier 만 싣고, 로그·이벤트·condition 에는 둘 다 없다.

const (
	// scramIterations 는 새 verifier 의 반복 수다 — PG 기본 scram_iterations.
	scramIterations = 4096
	// scramSaltLen 은 새 verifier 의 salt 바이트 수다 — PG SCRAM_DEFAULT_SALT_LEN.
	scramSaltLen = 16
	// scramMechanism 은 PG SCRAM verifier 의 머리다: SCRAM-SHA-256$<iter>:<salt>$<StoredKey>:<ServerKey>.
	scramMechanism = "SCRAM-SHA-256"
	// scramClientKey·scramServerKey 는 RFC 5802 의 HMAC 키 이름이다.
	scramClientKey = "Client Key"
	scramServerKey = "Server Key"

	// superuserName 은 superuser·replication 역할이다 — 인스턴스 매니저가 user=postgres 로 붙는다.
	superuserName = "postgres"
	// superuserUsernameKey·superuserPasswordKey 는 Secret 키다(basic-auth 형식).
	superuserUsernameKey = "username"
	superuserPasswordKey = "password"

	// superuserPasswordEnv 는 부트스트랩이 passfile 을 쓰는 비밀번호 env 다 — 복제 클러스터의 PRIMARY_PASSWORD 와 다른 이름.
	superuserPasswordEnv = "LOCAL_SUPERUSER_PASSWORD"
	// pgPassFileEnv 는 libpq 가 읽는 passfile 경로 env 다.
	pgPassFileEnv = "PGPASSFILE"

	// storedVerifierQuery 는 postgres 역할의 verifier 다 — NULL 은 빈 줄.
	storedVerifierQuery = "SELECT coalesce(rolpassword, '') FROM pg_authid WHERE rolname = '" + superuserName + "'"
	// memberSQLTimeout 은 멤버 SQL exec 1회의 상한이다 — 무응답 exec 가 reconcile 을 붙잡지 않는다.
	memberSQLTimeout = statusPollInterval

	// superuserAuthUpdatedReason 은 비밀번호를 Secret 에 맞췄을 때의 이벤트 사유다.
	superuserAuthUpdatedReason = "SuperuserAuthUpdated"
)

// errNotSCRAM 은 SCRAM verifier 가 아닌 rolpassword(NULL·md5)다.
var errNotSCRAM = errors.New("stored password is not a SCRAM-SHA-256 verifier")

// superuserAuth 는 샤드 하나의 superuser 인증 대조 결과다. ready 면 그 샤드의 설정 CM·STS 를 반영해도 된다.
// reason 이 있으면 SuperuserAuthPending 사유다 — 멤버가 없는 샤드는 ready 이면서 Secret 문제를 알린다.
type superuserAuth struct {
	ready  bool
	reason string
	detail string
}

// shardAuth 는 샤드 이름을 단 대조 결과다(condition 메시지용).
type shardAuth struct {
	shard string
	superuserAuth
}

// unverified 는 멤버가 도는데 비밀번호를 확인하지 못한 결과다 — 설정·템플릿을 미룬다.
func unverified(detail string) superuserAuth {
	return superuserAuth{reason: ReasonSuperuserAuthUnverified, detail: detail}
}

// ensureSuperuserAuth 는 샤드 primary 의 postgres verifier 를 Secret 비밀번호에 맞춘다(설계 §3.18):
//
//	복제 클러스터             → 대상 밖(역할은 원천 클러스터 것 — standby 는 ALTER 못 한다)
//	멤버 파드 0(새 샤드·정지) → ready — 대조할 곳이 없다(미루면 새 클러스터가 안 생긴다). Secret 문제는 사유로만
//	Secret 못 씀              → 미룬다
//	실제 primary 없음·exec 실패 → 미룬다(모름)
//	verifier 가 Secret 것     → ready
//	다름·NULL                 → 새 salt verifier 로 ALTER ROLE → 재대조 → 같으면 ready + SuperuserAuthUpdated
func (r *PostgresClusterReconciler) ensureSuperuserAuth(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	ref shardRef,
) (superuserAuth, error) {
	if standaloneReplicaEnabled(cluster) {
		return superuserAuth{ready: true}, nil
	}
	password, problem, err := r.superuserPassword(ctx, cluster)
	if err != nil {
		return superuserAuth{}, err
	}
	pods, err := r.shardMembers(ctx, cluster, ref.member)
	if err != nil {
		return superuserAuth{}, err
	}

	// 대조할 멤버가 없다 — 첫 primary 가 뜨면 다음 reconcile 이 맞춘다.
	if len(pods) == 0 {
		problem.ready = true
		return problem, nil
	}
	if problem.reason != "" {
		return problem, nil
	}

	pod := r.authPrimary(ctx, cluster, ref, pods)
	if pod == nil {
		return unverified("no real primary to read the postgres verifier from"), nil
	}
	stored, err := r.storedVerifier(ctx, pod)
	if err != nil {
		log.FromContext(ctx).V(1).Info("postgres verifier unreadable", "pod", pod.Name, "error", err)
		return unverified("postgres verifier unreadable on " + pod.Name), nil
	}
	if same, _ := verifierMatches(stored, password); same {
		return superuserAuth{ready: true}, nil
	}

	// 다르거나 NULL — 새 salt verifier 를 싣고 다시 읽는다. 평문은 exec 에 싣지 않는다.
	salt := make([]byte, scramSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return superuserAuth{}, fmt.Errorf("scram salt: %w", err)
	}
	verifier, err := scramVerifier(password, salt, scramIterations)
	if err != nil {
		return superuserAuth{}, err
	}
	if err := r.setVerifier(ctx, pod, verifier); err != nil {
		// exec 오류 글은 싣지 않는다 — psql 이 문장(verifier)을 되풀이할 수 있다.
		log.FromContext(ctx).Info("ALTER ROLE postgres failed", "pod", pod.Name)
		return unverified("ALTER ROLE postgres failed on " + pod.Name), nil
	}
	stored, err = r.storedVerifier(ctx, pod)
	if err != nil {
		return unverified("postgres verifier unreadable on " + pod.Name), nil
	}
	if same, _ := verifierMatches(stored, password); !same {
		return unverified("postgres verifier still differs from the Secret on " + pod.Name), nil
	}
	commonsevents.Emitf(r.Recorder, cluster, superuserAuthUpdatedReason,
		"postgres password set from Secret %s on %s (SCRAM verifier)", cluster.Spec.SuperuserSecretRef.Name, pod.Name)
	return superuserAuth{ready: true}, nil
}

// superuserPassword 는 CR 이 가리키는 Secret 의 비밀번호다 — username 은 postgres, password 는 비지 않은 인쇄 가능
// ASCII(PG SASLprep 가 그대로 두고 passfile 한 줄에 들어간다). 못 쓰면 비밀번호 대신 Pending 사유를 준다.
func (r *PostgresClusterReconciler) superuserPassword(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
) (string, superuserAuth, error) {
	ref := cluster.Spec.SuperuserSecretRef
	if ref == nil || ref.Name == "" {
		return "", superuserAuth{reason: ReasonSuperuserSecretUnset, detail: "spec.superuserSecretRef is empty"}, nil
	}
	var secret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: ref.Name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", superuserAuth{reason: ReasonSuperuserSecretMissing, detail: "Secret " + ref.Name + " not found"}, nil
		}
		return "", superuserAuth{}, fmt.Errorf("get superuser Secret %q: %w", ref.Name, err)
	}
	if string(secret.Data[superuserUsernameKey]) != superuserName {
		return "", superuserAuth{reason: ReasonSuperuserSecretInvalid,
			detail: "Secret " + ref.Name + " data.username must be " + superuserName}, nil
	}
	password := string(secret.Data[superuserPasswordKey])
	if !printableASCII(password) {
		return "", superuserAuth{reason: ReasonSuperuserSecretInvalid,
			detail: "Secret " + ref.Name + " data.password must be non-empty printable ASCII"}, nil
	}
	return password, superuserAuth{}, nil
}

// printableASCII 는 s 가 비지 않았고 0x20–0x7E 로만 되었는지다 — 개행·제어 문자는 passfile 줄을 깨고, 비-ASCII 는
// SASLprep 가 바꿀 수 있다.
func printableASCII(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

// authPrimary 는 verifier 를 읽고 쓸 실제 primary 다 — 런타임 CM primary 를 먼저, 그다음 primary 를 보고하는
// 멤버를 이름 순으로 확인한다(L1 첫 reconcile 엔 CM 이 없을 수 있다). 없으면 nil.
func (r *PostgresClusterReconciler) authPrimary(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	ref shardRef,
	pods []corev1.Pod,
) *corev1.Pod {
	fenced, err := r.fencedPVCs(ctx, cluster.Namespace)
	if err != nil {
		return nil
	}
	now := time.Now()
	var candidates []*corev1.Pod
	if rt, found, err := r.readRuntime(ctx, cluster, ref.name); err == nil && found {
		if pod := podNamed(pods, endpointPod(rt.PrimaryEndpoint)); pod != nil {
			candidates = append(candidates, pod)
		}
	}
	for i := range pods {
		if claimsPrimary(&pods[i], fenced, now) && (len(candidates) == 0 || candidates[0].Name != pods[i].Name) {
			candidates = append(candidates, &pods[i])
		}
	}
	for _, pod := range candidates {
		if r.checkPrimary(ctx, pod, fenced, now) == primaryConfirmed {
			return pod
		}
	}
	return nil
}

// storedVerifier 는 postgres 역할의 rolpassword 다(NULL 은 "").
func (r *PostgresClusterReconciler) storedVerifier(ctx context.Context, pod *corev1.Pod) (string, error) {
	return r.memberSQL(ctx, pod, storedVerifierQuery)
}

// setVerifier 는 verifier 를 postgres 역할에 싣는다 — PG 는 SCRAM 형식 문자열을 다시 해시하지 않고 그대로 둔다.
func (r *PostgresClusterReconciler) setVerifier(ctx context.Context, pod *corev1.Pod, verifier string) error {
	_, err := r.memberSQL(ctx, pod, "ALTER ROLE "+superuserName+" PASSWORD "+sqlLiteral(verifier))
	return err
}

// memberSQL 은 멤버 로컬 소켓으로 SQL 한 문장을 돌린다(psql -At, 앞뒤 공백 제거).
func (r *PostgresClusterReconciler) memberSQL(ctx context.Context, pod *corev1.Pod, sql string) (string, error) {
	if r.PromotionPodExecutor == nil {
		return "", errors.New("pod executor is not configured")
	}
	probeCtx, cancel := context.WithTimeout(ctx, memberSQLTimeout)
	defer cancel()
	out, err := r.PromotionPodExecutor.Exec(probeCtx, memberTarget(pod),
		[]string{"psql", "-U", superuserName, "-d", "postgres", "-Atqc", sql})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// scramVerifier 는 password 의 PG SCRAM-SHA-256 verifier 다(RFC 5802·7677):
//
//	SaltedPassword = PBKDF2-HMAC-SHA-256(password, salt, iterations)
//	StoredKey = SHA-256(HMAC(SaltedPassword, "Client Key")) · ServerKey = HMAC(SaltedPassword, "Server Key")
//
// ASCII 비밀번호는 SASLprep 가 그대로 둔다(PG pg_saslprep 의 ASCII 지름길).
func scramVerifier(password string, salt []byte, iterations int) (string, error) {
	storedKey, serverKey, err := scramKeys(password, salt, iterations)
	if err != nil {
		return "", err
	}
	enc := base64.StdEncoding
	return fmt.Sprintf("%s$%d:%s$%s:%s", scramMechanism, iterations,
		enc.EncodeToString(salt), enc.EncodeToString(storedKey), enc.EncodeToString(serverKey)), nil
}

// scramKeys 는 StoredKey·ServerKey 다.
func scramKeys(password string, salt []byte, iterations int) ([]byte, []byte, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return nil, nil, fmt.Errorf("scram salted password: %w", err)
	}
	clientKey := hmacSHA256(salted, scramClientKey)
	storedKey := sha256.Sum256(clientKey)
	return storedKey[:], hmacSHA256(salted, scramServerKey), nil
}

func hmacSHA256(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

// verifierMatches 는 PG verifier 가 password 의 것인지다 — 그 salt·iteration 으로 다시 계산해 두 키를 상수 시간
// 비교한다. SCRAM verifier 가 아니면(NULL·md5·깨짐) errNotSCRAM 이다.
func verifierMatches(stored, password string) (bool, error) {
	mechanism, rest, _ := strings.Cut(stored, "$")
	params, keys, _ := strings.Cut(rest, "$")
	rawIter, rawSalt, _ := strings.Cut(params, ":")
	rawStored, rawServer, _ := strings.Cut(keys, ":")
	iterations, iterErr := strconv.Atoi(rawIter)
	salt, saltErr := base64.StdEncoding.DecodeString(rawSalt)
	storedKey, storedErr := base64.StdEncoding.DecodeString(rawStored)
	serverKey, serverErr := base64.StdEncoding.DecodeString(rawServer)
	if mechanism != scramMechanism || iterations <= 0 || len(salt) == 0 ||
		errors.Join(iterErr, saltErr, storedErr, serverErr) != nil {
		return false, errNotSCRAM
	}

	wantStored, wantServer, err := scramKeys(password, salt, iterations)
	if err != nil {
		return false, err
	}
	return hmac.Equal(storedKey, wantStored) && hmac.Equal(serverKey, wantServer), nil
}

// setSuperuserAuthCondition 은 샤드 대조 결과를 condition SuperuserAuthPending 으로 보인다(KSM → O1 경보).
func setSuperuserAuthCondition(cluster *postgresv1alpha1.PostgresCluster, auths []shardAuth) {
	if standaloneReplicaEnabled(cluster) {
		setCondition(&cluster.Status.Conditions, ConditionSuperuserAuthPending, metav1.ConditionFalse,
			ReasonNotApplicable, "replica cluster: roles come from the source cluster", cluster.Generation)
		return
	}
	reason := ""
	var pending []string
	for _, a := range auths {
		if a.reason == "" {
			continue
		}
		if reason == "" {
			reason = a.reason
		}
		pending = append(pending, a.shard+": "+a.detail)
	}
	if reason == "" {
		setCondition(&cluster.Status.Conditions, ConditionSuperuserAuthPending, metav1.ConditionFalse,
			ReasonSuperuserAuthVerified, "postgres password matches the superuser Secret on every running shard", cluster.Generation)
		return
	}
	setCondition(&cluster.Status.Conditions, ConditionSuperuserAuthPending, metav1.ConditionTrue, reason,
		"config and pod template of running shards wait for the postgres password: "+strings.Join(pending, "; "),
		cluster.Generation)
}
