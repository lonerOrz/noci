# action/ 目录优化修复计划

## 问题清单（按优先级）

### 🔴 高优先级

**#5 `post.js` — stdin 写入无 error handling**
- 位置：`action/src/post.js:109-110`
- 问题：`proc.stdin.write()` 不监听 `error` 事件，buffer 满时不调用 `drain` 就调 `end()`，写入失败会导致进程挂起且 Promise 不 reject
- 修复：添加 `stdin.on("error", ...)` 监听，`write()` 返回 false 时等待 `drain` 再 `end()`，追加末尾换行符确保 `readStdinPaths` 正确解析
- 文件：`action/src/post.js`

**#7 `proxy.js` — `--no-upstream` 硬编码，无配置项**
- 位置：`action/src/proxy.js:34-41`
- 问题：`proxy` 子进程始终传 `--no-upstream`，用户无法覆盖此行为
- 修复：在 `action.yml` 新增 `no-upstream` input（默认 true），传递给 proxy 命令
- 文件：`action/src/proxy.js`、`action/action.yml`

### 🟡 中优先级

**#3 `proxy.js:56` — 乱码注释**
- 问题：`//  silently退化成 non-cached mode.` 混入中文字符
- 修复：改为纯英文 `// silently degrade to non-cached mode.`
- 文件：`action/src/proxy.js`

**#8 `post.js` — cleanup 未清理 proxy 临时文件**
- 位置：`action/src/post.js:118-127`
- 问题：`hook-log-path` 和 `hook-script-path` 被清理，但 `/tmp/noci-proxy-${suffix}.log` 和 `/tmp/noci-proxy-${suffix}.port` 未清理
- 修复：在 cleanup 函数中一并删除 proxy log 和 port file
- 文件：`action/src/post.js`

### 🟢 低优先级

**#6 `config.js` — `NOCI_SIGNING_KEY` 有条件导出**
- 位置：`action/src/config.js:43`
- 问题：`if (signingKey) utils.exportVariable(...)` 空字符串时不导出，post.js 依赖环境变量判断
- 修复：无条件导出所有变量，post.js 改为检查值是否为空
- 文件：`action/src/config.js`、`action/src/post.js`

**#9 `index.js` — 无 SIGTERM 处理**
- 位置：`action/src/index.js`
- 问题：主进程收到 SIGTERM 时 proxy 子进程可能 orphan
- 修复：添加 `process.on("SIGTERM", ...)` 和 `process.on("SIGINT", ...)`，转发信号给 proxy 子进程
- 文件：`action/src/index.js`

---

## 执行顺序

1. **#5 post.js stdin error handling** ← 当前
2. #7 proxy no-upstream config
3. #3 proxy.js 乱码注释
4. #8 post.js cleanup 遗漏文件
5. #6 config.js 无条件导出 signingKey
6. #9 index.js SIGTERM handler
