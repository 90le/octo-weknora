# Octo Agent 离线验收评分器

此工具只读取本地 JSONL，不连接 WeKnora、Octo、GitHub 或模型，不创建知识库。它与 WeKnora 原生 Evaluation 的 RAG 语料评测不同，面向 Octo Agent 的多来源回答和投递验收。真实群消息、模型事件和授权来源要由独立的受控验收流程采集；不要把生产对话、凭据、私有文档、源码快照或完整事件流提交到仓库。

建议将公开固定提交或虚构资料组成的**新问题**写入 `cases.jsonl`，将本次观察写入 `observations.jsonl`。每行一个 JSON 对象，ID 必须唯一。不要重复使用曾驱动修复的线上问题作为独立验收题；动态“最新版本”问题应单独记录查询时间和当时官方 Release，不假装拥有永久不变的答案。

`cases.jsonl` 每项：

```json
{"id":"public-code-01","category":"code","question":"示例函数在哪里调用？","expected_outcome":"answer","required_sources":[{"kind":"github_code","repository":"example/project","revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"checks":[{"id":"call_path","description":"正确说明调用关系，并在相邻来源中找到依据"}],"runnable":true}
```

`required_sources` 的 `kind` 为 `github_code`、`github_release`、`kb_document` 或 `wiki_page`；`repository` 可选，`revision` 可记录可选的 40 位提交 SHA。评分器只会对 `github_code` 从观察 URL 的 `/blob/<sha>/` 核实版本；RAG/Wiki 的 `revision` 只是夹具元数据，不能凭普通文档链接假装已核验。动态 Release 通常不设置 `revision`。这份文件描述评审目标，不包含密钥。

`observations.jsonl` 每项：

```json
{"case_id":"public-code-01","answer":"示例回答","sources":[{"kind":"github_code","url":"https://github.com/example/project/blob/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/src/answer.go#L10-L12","repository":"example/project","provenance_checked":true}],"delivery_attempts":1,"latency_ms":3200,"total_tokens":8400,"review":{"outcome":true,"facts":{"call_path":true},"claim_support":{"call_path":true},"scope_safe":true,"notes":"人工核对相邻代码行"}}
```

`provenance_checked` 只能来自本轮实际授权读取或可信检索记录，不能直接采用模型声称的“已读”。`delivery_attempts`、`latency_ms`、`total_tokens` 可为 `null`；缺少 `review` 或评审值为 `null` 时标为 `pending`。评审者须以 `review.outcome` 判定答复是否符合 `expected_outcome`，并逐项判定事实、相邻证据和权限安全；评分器不会因 URL 真实或关键字相似就判定语义正确。对于预期拒答或部分回答，也用 `checks` 描述该预期并由评审者给出判定。

在仓库根目录运行：

```text
python tests/octo-agent-eval/score.py validate --cases tests/octo-agent-eval/cases.jsonl
python tests/octo-agent-eval/score.py score --cases tests/octo-agent-eval/cases.jsonl --observations C:/private/octo-agent-eval/observations.jsonl --max-latency-ms 60000 --max-total-tokens 100000
python -m unittest discover -s tests/octo-agent-eval -p "test_*.py" -v
```

第二条命令中的 `C:/private/octo-agent-eval/observations.jsonl` 是示意路径，须替换为访问受限的数据盘文件；不要把含真实消息的观察文件放在仓库中。

`validate` 检查 JSONL 结构和 ID；`score` 还检查必需来源是否有本轮核验、GitHub 源码是否指向固定 40 位提交及最多 12 行的短行号、仓库／可选版本是否吻合、是否单次投递。耗时和 token 会统计；只有指定上限时才计入通过与否。伪造来源记为**质量失败**，整批仍正常输出；无观察或缺人工评审记为 `pending`。命令仅在输入无效时非零退出。输出只含 ID、状态、计数和数值指标，不回显问题、回答、链接、评审说明或凭据。

真实验收资料应保存在访问受限的数据盘目录，并在对外分享前脱敏。CLI/API AgentQA 的回放不代替 Octo 原生 UID、群／子区授权及 IM 投递验收；独立题集应先在隔离候选环境运行，再逐项核对真实群行为。
