# 授权说明 / Licensing

PanTransfer 的**协议层与业务编排逻辑**派生自两个上游项目，因此本仓库整体
**不是**可按普通开源许可证自由使用的代码。下面是准确的权利边界。

## 上游来源与各自条款

### 1. LitePan — PolyForm Noncommercial License 1.0.0

- 项目：<https://github.com/jimboo7339/LitePan>
- 条款：<https://polyformproject.org/licenses/noncommercial/1.0.0>
- 实际使用的部分：`drivers/quark/*` 与 `drivers/guangya/*` 的协议实现
  （请求签名、反爬参数、分享 token、转存任务轮询、目录解析），
  文件内以「来自Trae」标注。

### 2. cloud-auto-save-x（CASX）— 开源版适配器

- 项目：cloud-auto-save-x 1.x 开源版 / CASX v1.0.9 开源源码
- 实际使用的部分：`internal/transfer/magic_rename.go`（魔法重命名）、
  `internal/transfer/executor.go`（追剧执行器）、
  `internal/transfer/preview.go`（分享预览）。
  这些文件是 Python 源码的 Go 重写，文件头注明了原始文件名。

## 这意味着什么

PolyForm Noncommercial 1.0.0 允许：

| 行为 | 是否允许 |
|---|---|
| 个人研究、实验、学习 | ✅ |
| 家庭/个人非商业使用 | ✅ |
| 修改代码、基于它创作新作品 | ✅（仅限非商业用途） |
| 慈善组织 / 教育机构 / 公共研究机构 / 政府使用 | ✅（条款明确列为非商业） |
| 分发副本 | ✅（但必须附带原条款 + 保留 Required Notice） |
| **商业性使用** | ❌ 需另行获得授权 |
| **再许可（sublicense）或转让许可** | ❌ 条款明文禁止 |

也就是说：**你可以自用、可以改、可以分享给朋友，但不能拿它做商业产品**，
也不能把这份代码以"我的许可证"的名义再授权给别人。

## 必要的版权声明

按 PolyForm Noncommercial 的 Notices 条款，分发时必须保留：

```
Required Notice: Copyright Ponphil (2026)
Required Notice: Copyright LitePan Authors (2026)
```

以及本文件所述条款的副本或其 URL。

## 与其它依赖的关系

本项目的 Go / Vue 依赖各自遵循其原有许可证（MIT / BSD / Apache-2.0 /
ISC 等，见各依赖包的 LICENSE 文件）。这些依赖的条款与上游的
PolyForm Noncommercial **相互独立**——依赖宽松不意味着本项目整体宽松。

## 我（本仓库维护者）的立场

我做这一版改造时没有获得上游作者的商业授权，因此**不能**给 PanTransfer
选一个比 PolyForm Noncommercial 更宽松的许可证。

如果你的用途是商业性的，正确路径是联系上游作者获取授权，
而不是修改本文件或删掉这段说明。

## 常见问题

**Q：我只是自己在家用，可以吗？**
A：可以。个人非商业使用明确在允许范围内。

**Q：我想在公司内部部署，可以吗？**
A：看情况。如果是"公司内部自用、不对外提供产品"，属于机构内部使用，
通常不构成"商业性使用"；但如果是为了盈利或对外提供服务，
就需要向上游取得授权。这一点建议自己判断或咨询法务。

**Q：我能把改好的版本发到公司 Git 上吗？**
A：分发副本是允许的，但必须同时附带原条款与 Required Notice，
并且不能声称这是你自己独立编写的代码。

**Q：我要基于它做商业产品怎么办？**
A：联系 LitePan 作者（Ponphil）获取 PolyForm Noncommercial 的商业授权。