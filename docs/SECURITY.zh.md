<p align="center">
  <a href="SECURITY.md">English</a> |
  <a href="SECURITY.ko.md">한국어</a> |
  <a href="SECURITY.ja.md">日本語</a> |
  <b>中文</b>
</p>

# 安全策略 (中文)

> 英文原文: [SECURITY.md](SECURITY.md) — canonical / 正本

## 支持的版本

| Version | 安全补丁支持 |
|---|---|
| 最新 minor (v0.x) | ✅ |
| 前一个 minor | ✅ (60 天) |
| 更早版本 | ❌ |

long-term-support (LTS) 策略将在 v1.0.0 GA 发布之后单独公布。

## 报告漏洞

**请勿在公开 issue 中提交安全漏洞。** 请使用私有渠道:

- 邮件: `security@keiailab.io`
- PGP 密钥指纹: `89A4 0947 6828 CB99 2338  C378 651E 51AF 520B CB78`
  (keiailab Helm chart 签名密钥 —— 与 gh-pages 上发布的 `artifacthub-repo.yml` 中的指纹一致)。

## 响应流程

1. 收到后 **48 小时内确认**。
2. **7 天内进行影响 / 严重度评估** (CVSS v3.1)。
3. 共享 **协商一致的补丁时间表** (通常 14–30 天)。
4. 公开披露前 **90 天 embargo** (可协商)。
5. **CVE 分配** 与 GitHub Security Advisory 发布。
6. **报告者署名致谢** (可选)。

## 披露策略

- 安全公告随补丁版本一同发布。
- 在必要时分配 CVE。
- 报告者署名致谢 (可选,opt-in)。
- 为受影响用户提供迁移指引。

## 安全运行建议

运行本 operator 时:

- 强制 TLS: `network.tls.mode=required`。
- 推荐: `network.networkPolicy.enabled=true` 启用 Network Policy。
- 强制 SCRAM-SHA-256 认证 (默认)。
- 使用 cert-manager 集成进行 Secret 轮换。
- 跟踪受支持矩阵中的 PostgreSQL 最新补丁版本。
- 使用 `cosign verify` 验证容器镜像。

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
