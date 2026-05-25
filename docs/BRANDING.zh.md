<p align="center">
  <a href="BRANDING.md">English</a> |
  <a href="BRANDING.ko.md">한국어</a> |
  <a href="BRANDING.ja.md">日本語</a> |
  <b>中文</b>
</p>

# 品牌指南 — `postgres-operator` (中文)

> 英文原文: [BRANDING.md](BRANDING.md) — canonical / 正本

> `postgres-operator` 的视觉 identity、voice 与 tone。

本文档为 `postgres-operator` 品牌决策的正本 (canonical) 参考。它适用于 README、发布说明、市场材料,以及代表本项目的所有 third-party 沟通。

## 1. Identity

**组织**: [keiailab](https://keiailab.com) —— Kubernetes 原生数据平台 (Apache-2.0、license-clean、与 vanilla upstream 兼容)。

**项目**: `postgres-operator` —— Kubernetes 上的 Apache-2.0 PostgreSQL Operator —— vanilla PG18+、license-clean、K8s 原生自动分片路线图。

## 2. 标志与视觉资产

| Asset | URL | 用途 |
|---|---|---|
| Primary logo (SVG) | `https://keiailab.com/assets/logo.svg` | README 标题、幻灯片 |
| Mono mark | `https://keiailab.com/assets/mark.svg` | favicon、社交卡片 |
| Wordmark | `https://keiailab.com/assets/wordmark.svg` | Footer、暗色背景 |

**标志放置**: README 顶部居中、width 120px。始终链接到 <https://keiailab.com>。

**安全间距 (Clear space)**: 标志周围最小 padding 为标志宽度的 25%。

**禁止事项**:
- 重新着色标志
- 添加投影或滤镜
- 放置于对比度不足的背景上
- 未获 keiailab 品牌批准而与其他标志组合

## 3. 配色

| 角色 | Hex | 用途 |
|---|---|---|
| Primary (keiailab teal) | `#0EA5A8` | 标题、主要操作、链接 |
| Secondary (deep navy) | `#0F172A` | 暗色背景、代码块 |
| Accent (warm amber) | `#F59E0B` | 高亮、徽章 accent |
| Neutral grey | `#64748B` | 浅色背景的正文文本 |
| Background light | `#F8FAFC` | 文档页面背景 |
| Background dark | `#020617` | 代码编辑器主题、dark mode |

GitHub README 的 shield.io 徽章推荐采用以上 hex。

## 4. 字体

- **标题**: System default (GitHub 的 default `-apple-system, BlinkMacSystemFont, Segoe UI, ...`)
- **正文**: 同上 (与 GitHub 原生一致)
- **代码**: `ui-monospace, SFMono-Regular, Consolas, ...` (GitHub 的 default monospace)

不使用额外 webfont (与 GitHub README 渲染一致)。

## 5. Voice & Tone

**读者**: Kubernetes 平台工程师 / DBA / SRE。

**Voice 原则**:
- **直接 (Direct)** —— 尽可能用 bullet 代替段落
- **基于证据 (Evidence-based)** —— 主张需附带基准测试 / SLA / 链接
- **厂商中立 (Vendor-neutral)** —— 参考上游 PostgreSQL,但不嵌入或封装 third-party operator
- **License-aware** —— 仅依赖 Apache-2.0 + BSD/MIT/PG-license

**应避免**:
- 营销式最高级 ("blazing fast"、"revolutionary"、"best-in-class")
- 模糊比较 ("X-class quality") —— *请用具体指标或基准来加以限定*
- 路线图中的日期截止 (改用 `standards/roadmap.md §1.1` 的 feature checklist)

## 6. README 标题标准

所有 README 的开头段落采用以下格式 (Wave 3 标准):

```markdown
<p align="center">
  <img src="https://keiailab.com/assets/logo.svg" alt="keiailab" width="120"/>
</p>

# postgres-operator

> **Apache-2.0 PostgreSQL Operator for Kubernetes — vanilla PG18+, license-clean, K8s-native auto-sharding roadmap**

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"/></a>
  <!-- 保留并对齐现有 shield.io 徽章 -->
</p>

<p align="center">
  <b>English</b> |
  <a href="README.ko.md">한국어</a> |
  <a href="README.ja.md">日本語</a> |
  <a href="README.zh.md">中文</a>
</p>
```

## 7. README Footer 标准

所有 README 与策略 .md 文件结尾附以下 footer:

```markdown
---

<p align="center">
  © 2026 keiailab · <a href="LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
```

## 8. Badges 标准顺序

README 的 shield.io 徽章排列 (左→右):

1. License (Apache-2.0)
2. Go Version (1.25+)
3. Database (PostgreSQL 18+)
4. Kubernetes Version (1.26+)
5. Container Image (ghcr.io/keiailab)
6. Helm Chart (Chart.yaml 版本 + Artifact Hub 链接)
7. OpenSSF Scorecard
8. GitHub Discussions

## 9. Discussions / Issues / PR Templates

- **Discussions**: `https://github.com/keiailab/postgres-operator/discussions` —— 功能创意、Q&A
- **Issues**: bug 报告 + 含用例的具体功能请求
- **PR template**: `.github/PULL_REQUEST_TEMPLATE.md` 标准 (必须引用用户场景 + 验证命令,见 `standards/checklist.md §3`)

## 10. Social & External

- **网站**: https://keiailab.com
- **GitHub Org**: https://github.com/keiailab
- **Artifact Hub** (Helm): https://artifacthub.io/packages/search?repo=keiailab-postgres-operator
- **GHCR** (Container): https://github.com/keiailab/postgres-operator/pkgs/container/postgres-operator

## 11. 许可证 & 致谢

- License: [Apache-2.0](../LICENSE)
- Copyright: © 2026 keiailab contributors
- Third-party 致谢: 如适用,见 [NOTICE](../NOTICE)

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
