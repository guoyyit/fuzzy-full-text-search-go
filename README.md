# fuzzy-full-text-search-go

一个**本地可运行、可持久化**的全文检索服务（Go，零第三方依赖）。接收带多个文本字段的
文档，提供关键词检索，核心能力是**拼写容错**：查询词即使有拼写错误，仍可按有界编辑
距离近似命中文档。匹配与排序完全确定、可解释；索引仅保存在本地文件（WAL），不依赖
任何外部搜索服务。

## 能力总览

| 需求 | 实现 |
| --- | --- |
| 新增 / 覆盖更新 / 删除，字段权重可配置 | `Upsert` / `Delete` / `Batch`；`Config.FieldWeights` |
| 拼写容错（有界编辑距离），偏差越大分越低 | `term~`、`term~N`，Levenshtein 距离，乘性衰减 |
| 短语、布尔、字段限定，并与容错组合 | `"a b"`、`AND/OR/NOT`、`field:…`、`field:(…)`、`"a b"~N` |
| 结果稳定可复现 | 固定评分公式 + 分数降序/文档 ID 升序双重排序 |
| 增量更新立即可见、查询读到一致快照 | 不可变快照 + copy-on-write 原子切换（MVCC） |
| 本地持久化、崩溃不丢已提交数据、不产生损坏 | 仅追加 WAL，长度前缀 + CRC32 + `fsync`，重放截断损坏尾 |
| 精确字符区间高亮（含近似命中） | 保存原始字节偏移 `[Start,End)`，不用归一化后的词反推 |
| 容错上限/权重/相关度参数可配，非法配置被拒绝 | `Config.Validate()`，启动与应用时校验并给出原因 |

## 快速开始

```bash
go build ./...
go test -race ./...          # 全部自动化测试

# HTTP 服务
go run ./cmd/fuzzsearch \
  -addr :8080 -data ./data \
  -weights title=3,body=1 \
  -max-edit-distance 2 -fuzzy-penalty 0.5
```

### HTTP API

| 方法与路径 | 说明 |
| --- | --- |
| `PUT /v1/documents/:id` | upsert，请求体 `{"fields":{"title":"...","body":"..."}}` |
| `GET /v1/documents/:id` | 按 ID 取文档 |
| `DELETE /v1/documents/:id` | 删除（删除不存在的 ID 不是错误） |
| `POST /v1/batch` | 原子批量，体为 `[{"delete":false,"doc":{…}}, …]` |
| `GET /v1/search?q=…` | 检索；也可 `POST /v1/search`，体 `{"query":"…"}` |
| `GET /v1/explain` | 返回容错边界等生效配置的文字说明 |

```bash
curl -X PUT localhost:8080/v1/documents/d1 \
  -d '{"fields":{"title":"Quick Start","body":"the quick brown fox"}}'
curl -X PUT localhost:8080/v1/documents/d2 \
  -d '{"fields":{"title":"Slow Start","body":"a quikc red fox"}}'

curl 'localhost:8080/v1/search?q=quikc~'            # 容错：命中 quick / quikc
curl 'localhost:8080/v1/search?q=body:"quick%20brown"'   # 字段内短语
curl 'localhost:8080/v1/search?q=fox%20AND%20NOT%20slow' # 布尔
```

### 作为 Go 库使用

```go
svc, err := fuzzsearch.Open(fuzzsearch.Config{
    FieldWeights:           map[string]float64{"title": 3, "body": 1},
    DefaultMaxEditDistance: 2,
    FuzzyPenalty:           0.5,
    IDFWeight:              1.0,
    FuzzyPrefixLength:      0,
    DataDir:                "./data",
})
if err != nil { log.Fatal(err) }   // 非法配置在打开任何文件前被拒绝
defer svc.Close()

svc.Upsert(fuzzsearch.Document{ID: "d1",
    Fields: map[string]string{"title": "Quick Start", "body": "quick brown fox"}})
res, err := svc.Search(`title:quick~ AND body:"brown fox"`)
```

## 查询语言

由 `query.go` 的递归下降解析器实现，语法（大小写：词项一律小写化；`AND/OR/NOT` 必须
大写，小写 `and/or/not` 是普通词）：

```
query   := orExpr
orExpr  := andExpr ( (OR | ||) andExpr | andExpr )*   # 相邻即隐式 OR
andExpr := unary  ( (AND | &&) unary )*
unary   := (NOT | ! | -) unary | primary
primary := '(' orExpr ')' | 字段限定? 短语 | 字段限定? 词项
短语    := '"' 词+ '"' ('~' 数字?)?
词项    := 词 ('~' 数字?)?
字段限定 := 名字 ':'，可作用于词项、短语或 '(' … ')' 分组
```

示例：

* `cat~` —— 容错命中，上限取配置默认值
* `cat~1` —— 显式上限 1；`cat~0` 等价于精确
* `title:cat~2` —— 只在 title 字段容错匹配
* `"quick brown"` / `"quikc brown"~2` —— 精确短语 / 每位置允许容错的短语
* `cat AND dog`、`cat OR dog`、`cat dog`（隐式 OR）、`cat AND NOT dog`、`NOT dog`
* `body:(cat OR dog)`、`(a OR b) AND NOT c`

解析错误（空查询、引号/括号不闭合、非法 `~x`、空字段名等）返回明确错误，不会静默。

## 匹配语义（容错边界的明确说明）

* **分词**：Unicode 字母/数字的最长连续段作为词（`analyze`），统一小写；同时记录每个
  token 在**原始字符串中的字节偏移**。
* **容错指标**：Levenshtein 编辑距离（插入/删除/替换各计 1，按 rune 计算，多字节安全）。
* **命中条件**：`t~N` 命中字段内任意距离 `d ≤ N` 的索引词。
  * 无 `~`：**只精确命中**（`d = 0`）；
  * `t~`：使用 `Config.DefaultMaxEditDistance`；
  * `t~N`：`N` 是硬上限，但**不得超过** `DefaultMaxEditDistance`（服务端钳制，防止调用方
    绕过全局容错策略）；
  * `Config.FuzzyPrefixLength > 0` 时，候选词与查询词前若干个 rune 必须完全相同；
  * 近似命中会在高亮中携带 `editDistance` 与实际命中的索引词，边界显式可见。
* **短语容错**：`"a b"~N` 中每个位置分别允许距离 ≤ N，位置必须连续；距离累加进短语得分。

## 排序（评分公式）

对一次“词项 × 字段 × 候选词”的命中，文档得分贡献为：

```
contrib = 字段权重
        × ( 1 + IDFWeight × IDF(候选词, 该字段) )
        × ( 1 + ln(tf) )
        × 1 / ( 1 + FuzzyPenalty × 编辑距离 )

IDF = ln(1 + (N − df + 0.5) / (df + 0.5))      # N=文档总数, df=该字段含该词的文档数
```

* 字段权重越大越靠前（命中 title 权重 3 的文档优先于只命中 body 权重 1 的文档）；
* 罕见词 IDF 更高；词频 `tf` 用饱和的对数项；
* **编辑距离越大，因子 `1/(1+FuzzyPenalty·d)` 越小，得分严格越低**（`FuzzyPenalty=0`
  时关闭该惩罚，由配置显式决定）；
* OR/AND 的总得分为各命中贡献之和；短语贡献按其各位置词的 IDF 之和与总距离计算。
* **最终顺序**：分数降序；分数相同按文档 ID 字典序升序。相同输入 + 相同索引快照 →
  结果集合与顺序逐字节一致（测试 `TestDeterministicOrder` 多次比对 JSON）。每个命中的
  `explain` 给出各字段的贡献分量，便于解释。

## 一致性（MVCC 快照）

* 索引由 `index` 持有一个不可变 `snapshot`（文档表、token 偏移表、倒排链）。
* 写操作先整体追加到 WAL 并 `fsync`，然后在**写锁内 copy-on-write** 构建新快照
  （只拷贝被触碰的 map，绝不原地改旧 map/切片），最后原子替换指针。
* `Search` 开始时抓取当前快照指针并全程只读旧快照。因此：
  * 提交后立即可见；
  * 正在执行的查询永远看到某一时刻的完整视图，**不可能看到半个批次**；
  * 读不阻塞写、写不阻塞读；写者之间串行化。
* 批量 `Batch` 是单个原子单元（同一条 WAL 记录、同一次快照切换）。

## 持久化与崩溃恢复

* 数据目录中只有一个追加文件 `fuzzy-search.wal`，每条记录是一个原子批次，帧格式：

  ```
  uint64 载荷长度 | uint32 CRC32(长度头 ‖ JSON载荷) | JSON 载荷
  ```

* `append` 成功返回前完成整帧写入与 `fsync`——已确认的提交不会丢。
* 启动时按顺序重放全部完整帧；遇到以下任一情况即认为是崩溃留下的**未同步尾部**并
  立即停止：短读（半截头/载荷/CRC）、长度越界、CRC 不符、JSON 非法。随后把文件
  **截断到最后一条完整帧边界**。因此：
  * 崩溃前所有已 fsync 的批次完整保留（见 `TestPersistReplay`）；
  * 半截/损坏尾部被丢弃，不会应用半条变更，也不会让索引损坏
    （见 `TestWALTornTail`、`TestWALCRCRejectsCorruptFrame`）；
  * 恢复后服务可继续正常追加写入。
* 直接重放整份 WAL 即可重建索引；数据规模增长时可在该格式前再加“快照 + 后续 WAL”，
  帧与 CRC 设计无需改变。

## 高亮

每个 `Highlight` 为 `{field,start,end,term,editDistance}`：

* `[start,end)` 是**字段原始字符串中的字节区间**，可直接 `field[start:end]` 切片，
  不做大小写/Unicode 反推，因此多字节文本也不会错位或越界；
* 精确命中 `editDistance=0`，近似命中为实际距离；
* 同区间多次命中去重并保留最小距离，整体按 `(字段, 起点, 终点)` 排序。

## 配置校验

`Config.Validate()`（`Open` 首先调用，非法时不创建任何文件）拒绝并给出原因：

* `FieldWeights` 为空、含空字段名、权重非有限数或 `<= 0`；
* `DefaultMaxEditDistance < 0`、`FuzzyPrefixLength < 0`；
* `IDFWeight < 0`、`FuzzyPenalty < 0`（含 NaN/Inf）；
* `DataDir` 为空。

文档中出现但未配置权重的字段会随文档保存，但不建索引、不参与检索与评分；查询限定到
未知字段时返回空结果而非报错。

## 代码结构

```
config.go      配置与校验
document.go    Document / Hit / Highlight 类型
analyzer.go    Unicode 分词 + 字节偏移
fuzzy.go       Levenshtein / 有界编辑距离（带状 DP 剪枝）
query.go       查询词法、递归下降解析、AST
eval.go        快照上的求值：容错、短语、布尔、评分、高亮
index.go       不可变快照、倒排索引、copy-on-write 原子提交
wal.go         fsync 追加日志、CRC、损坏尾截断重放
service.go     对外 API（Open/Upsert/Delete/Batch/Get/Search/Close）
cmd/fuzzsearch HTTP 服务
*_test.go      容错、加权、短语/布尔、增量、并发(-race)、崩溃恢复、高亮
```
