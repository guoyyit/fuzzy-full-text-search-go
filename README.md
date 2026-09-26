# fsearch — 本地可持久化的容错全文检索服务

一个零外部依赖、仅落盘到本地目录的全文检索服务。支持多字段文档、带字段权重的
相关性排序、**有界拼写容错**、短语 / 布尔 / 字段限定查询、精确字符区间高亮、
快照隔离的并发读写，以及崩溃安全（WAL + 快照）的本地持久化。

- 语言：Go（标准库实现，无第三方依赖）
- 形态：可嵌入的 Go 库（`internal/service`）+ 本地 HTTP/JSON 服务（`cmd/fsearch`）

---

## 1. 快速开始

```bash
# 构建
go build -o fsearch ./cmd/fsearch

# 使用示例配置启动（不传 -config 则用内置默认配置）
./fsearch -addr 127.0.0.1:8080 -data ./fsearch-data -config config.example.json
```

新增 / 覆盖更新文档（以 `id` 唯一标识）：

```bash
curl -s -X POST http://127.0.0.1:8080/documents \
  -H 'Content-Type: application/json' \
  -d '{"id":"d1","fields":{"title":"Quick Brown Fox","body":"a fast forest animal"}}'
```

容错检索（查询词 `quik` 能命中含 `quick` 的文档）：

```bash
curl -s 'http://127.0.0.1:8080/query?q=quik'
```

删除：`curl -s -X DELETE http://127.0.0.1:8080/documents/d1`
按路径覆盖：`curl -s -X PUT http://127.0.0.1:8080/documents/d1 -d '{...}'`

### HTTP 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/documents` | 新增/覆盖，body 为 `{id, fields}` |
| `PUT` | `/documents/{id}` | 按路径 ID 覆盖更新 |
| `DELETE` | `/documents/{id}` | 删除 |
| `GET` | `/query?q=...` | 查询（也支持 `POST` body `{"query":"..."}`） |
| `GET` | `/health` | 健康检查 |

返回的每个命中包含 `doc_id`、`score`、以及每个命中文段的得分、最小编辑距离和
用于高亮的字节区间 `ranges`。

---

## 2. 查询语法

| 写法 | 含义 |
| --- | --- |
| `word` | 单个关键词（默认是否容错由配置决定） |
| `field:word` | 仅在指定字段匹配 |
| `"a phrase"` | 短语：词条需在同一字段内顺序**相邻** |
| `field:"a phrase"` | 字段限定的短语 |
| `word~` | 强制允许该词容错（仍受 `max_edit_distance` 上限约束） |
| `word$` | 强制该词精确匹配（不接受容错） |
| `a AND b` | 布尔与（优先级高于 OR） |
| `a OR b`、`a b` | 布尔或；相邻词默认按 OR 连接 |
| `NOT a`、`-a` | 排除命中 `a` 的文档；可与 AND 组合，如 `a AND NOT b` |
| `(a OR b) AND c` | 括号分组 |

要点：

- **容错与短语/布尔/字段限定可正确组合**。例如 `"broen fox"` 会在短语的每个
  词位上分别应用有界容错；`title:quik AND NOT tag:old` 中字段限定与排除并存。
- `~` 只能“允许”容错，**不能突破配置的距离上限**；`$` 可在默认容错时强制精确。
- 查询在解析阶段做大小写归一化与分词，与索引侧使用同一套分词规则。

---

## 3. 匹配与排序语义（结果稳定可解释）

### 3.1 分词与归一化

- 词条由 Unicode 字母/数字构成，其余字符为分隔符；统一转小写。
- 每个 token 记录其在**原文字段值中的字节区间**（左闭右开），高亮直接基于该区间。

### 3.2 有界拼写容错

- 近似命中使用 **Levenshtein 编辑距离**（插入/删除/替换各计 1，按 rune 计算），
  阈值由配置 `max_edit_distance` 给定（默认 1；设为 0 则完全关闭容错）。
- 距离带提前终止：长度差或当前行最小值一旦超过上限即判定不命中。
- **近似命中的边界是明确且可解释的**：只有 `编辑距离 <= max_edit_distance` 的
  文档词条才算命中；每个返回的字段命中都带有 `edit_distance`（0 表示精确）。

### 3.3 打分公式

单字段的一次词条命中基础得分：

```
score(field) = weight(field) × (1 − fuzzy_penalty × editDistance)
```

- `weight` 来自 `field_weights`，未配置字段使用 `default_field_weight`；
  因此命中高权重字段的文档自然排在前面。
- 精确命中 `editDistance = 0` 不打折；**偏差越大，折扣越多、得分越低**。
- 同一字段多次出现同一词，得分只计一次（避免靠词频刷分），但高亮区间会标注
  该字段内全部满足距离上限的出现位置。

短语命中：先对短语内每个词按其自身距离计算 `(1 − penalty×d)`，取均值后乘以
字段权重，再乘上邻近度加成：

```
score = weight × avg_k(1 − penalty × d_k) × (1 + phrase_proximity_bonus)
```

文档总分为各命中文段贡献之和；`NOT` 子句只过滤、不贡献得分。

### 3.4 稳定排序

- 先按总分**降序**；总分相同时按 `doc_id` 字典序**升序**。
- 全部遍历顺序确定（文档按插入顺序、字段名/词表排序后枚举），不依赖 map
  随机遍历。因此**相同查询 + 相同索引状态，返回集合与顺序逐字节一致**；
  重启重建索引后结果同样一致（有测试覆盖）。

---

## 4. 一致性与并发（快照隔离）

- 索引采用不可变快照 + copy-on-write：每次新增/覆盖/删除都基于当前快照复制出
  一份新快照，旧快照永不修改。
- 写操作在一把写锁内串行完成「构造新快照 → WAL 追加并 fsync → 原子发布」；
  进行中的查询持有的是发布前的旧快照，**永远不会读到部分提交**。
- 查询在某一时刻的快照上执行，期间发生的写入对本次查询不可见、对后续查询可见。
- 全部路径通过 `go test -race` 并发读写测试。

### 提交可见性

`Upsert/Delete` 返回成功即表示：记录已追加到 WAL 并完成 fsync，且新快照已发布，
随后的任何查询都能看到该变更。

---

## 5. 本地持久化与崩溃恢复

数据目录包含两个文件：

- `snapshot.json`：全量文档快照。通过 `写临时文件 → fsync → 原子 rename →
  fsync 目录` 发布，任何时刻只会是“上一个完整版本”或“新版本”，不会是中间态。
- `wal.log`：只追加预写日志。帧格式为 `魔数 + 序号 + 操作类型 + CRC32 + 长度
  + JSON 体`，**每帧追加后立即 fsync**。

恢复流程（打开服务时）：

1. 读取 `snapshot.json`（若存在）；
2. 顺序重放 `wal.log` 中其后的完整帧（upsert/delete 按序应用）；
3. WAL 末尾因崩溃留下的**半截帧 / CRC 失败尾帧会被安全截断**（它从未被确认）；
   若损坏帧之后仍存在完整帧（中间损坏），则**直接报错拒绝启动**而不是静默跳过，
   避免丢失可能已确认的提交。

保证：

- **已确认提交不丢**：返回成功的写都已 fsync，崩溃/断电后重放恢复。
- **不产生损坏状态**：快照原子发布、WAL 校验 + 尾帧截断。
- 进程被直接杀死（不优雅退出）也能恢复（有 `TestCrashWithoutClose` 覆盖）。
- 正常 `Close()` 或手动 `Checkpoint()` 会落一次快照并截断 WAL，加快下次启动。

---

## 6. 高亮

- 每个命中文段返回 `ranges`：字段原文中的**字节区间**（`start` 为闭、`end` 为
  开），可直接 `fieldValue[start:end]` 切片标注。
- 近似命中标注的是**文档中实际命中的词条**（不是查询词），因此位置准确。
- 短语命中逐 token 标注；`NOT` 子句不产生高亮；区间会排序并合并重叠，保证不
  越界（对包含中文等多字节文本同样正确）。

---

## 7. 配置项与校验

| 字段 | 默认值 | 约束 |
| --- | --- | --- |
| `max_edit_distance` | `1` | `[0,10]`，0 关闭容错；过大被拒绝 |
| `field_weights` | `{}` | 字段名非空、无首尾空白；权重 `>= 0` |
| `default_field_weight` | `1.0` | `>= 0` |
| `fuzzy_penalty` | `0.3` | `(0,1]`，每单位编辑距离的扣分比例 |
| `phrase_proximity_bonus` | `0.5` | `[0,1]` |

非法配置在**启动时**（`service.Open`）即被拒绝并给出中文原因；非法文档
（空 ID 等）在写入时被拒绝。

---

## 8. 代码结构

```
cmd/fsearch          可执行 HTTP 服务入口
internal/model       文档模型
internal/config      配置与校验、默认值
internal/tokenize    带字节偏移的 Unicode 分词
internal/editdist    有界 Levenshtein 距离（提前终止）
internal/index       倒排索引 + 不可变快照（copy-on-write）
internal/query       查询词法/语法分析（短语、布尔、字段限定、~/$）
internal/search      AST 求值、容错匹配、打分、排序、高亮
internal/store       WAL + 快照持久化（CRC、fsync、原子 rename、崩溃恢复）
internal/service     门面：提交顺序、重放恢复、快照查询
internal/server      本地 HTTP/JSON 接口
```

## 9. 测试

```bash
go test ./...          # 全部用例
go test -race ./...    # 含竞态检测
```

覆盖：分词偏移、编辑距离边界、配置校验、快照隔离与覆盖/删除、解析器各类语法与
错误、容错排序低于精确、字段加权、短语与容错组合、布尔 AND/OR/NOT、增量更新、
并发读写、稳定排序、重启一致性、崩溃恢复、WAL 半截/CRC 损坏、以及精确/近似/
短语/多字节文本高亮。
