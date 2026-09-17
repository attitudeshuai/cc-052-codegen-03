# 农村合作社农产品溯源服务 · Farm Traceability Service

> 类型：后端 API 服务｜难度：★★★｜技术栈：**Go + Gin + PostgreSQL + MinIO（图片）+ Redis**（统一选型）

## 1. 一句话简介
给合作社每一批农产品发一个溯源码，从「谁家几号地块、哪天施肥打药、哪天采收、谁检测」一路查到消费者扫码看到的页面数据。

## 2. 真实场景与痛点
- 农产品想卖进平台/商超，第一关就是「有没有溯源」；合作社拿 Excel 记账，一查批次就断链。
- 消费者扫二维码看到的多是营销页，没有真数据。
- 十几个农户共用一台设备轮流上报，离线是常态，网络恢复后还要合并数据。

## 3. 目标用户
- 村级合作社、家庭农场、农产品区域公用品牌运营方。
- 想给自家水果贴溯源码的种植大户。

## 4. 核心功能（MVP）
1. **生产档案**：地块（地理坐标/面积/土壤）、作物、种植批次（`batch_id`）。
2. **农事记录上报**：施肥/用药/灌溉/除草，含时间、投入品名称、用量、操作人、照片；支持**离线批量补报**（客户端带 `client_uuid`，服务端幂等去重）。
3. **采收与检测**：采收日期、产量、农残检测报告（图片 + 结论），检测不合格批次直接锁定不可发码。
4. **溯源码生成**：一个批次拆成若干包装单位，批量生成唯一 `trace_code`（短码 + 校验位，防手输错误）。
5. **扫码查询**：公开只读接口，输入 code 返回溯源链（脱敏：不暴露农户手机号/精确坐标，只给村级位置）。
6. **基础资料**：投入品字典（农药登记证号、安全间隔期）。
7. **采集端 × 手工台账对账合并**：十几个采集端的补报记录与人工抄录的台账并成一份。按 **地块 + 作物 + 日期 + 操作人**（归一化后）认出同一件事；认出后默认保留更早登记的那条，字段不一致逐字段摆出来让人挑；两边打架时**有凭据（现场照片/签字单/报告编号）的一方优先**；合并前后总数必须对得上，人工取舍全程留档。

## 5. 进阶功能
- 安全间隔期校验：距上次施药不足间隔期就采收 → 返回 `WARNING` 并禁止发码。
- 二维码预生成 PDF 印刷文件（批量、含排版）。
- 防伪：一个码首次被扫记录首次扫码时间与地区，二次扫码提示。
- 数据导出给监管平台（JSON/XML 标准格式适配器）。

## 6. 接口设计（节选）
```
POST /api/v1/farms                          创建农场/合作社
POST /api/v1/plots                          地块登记
POST /api/v1/batches                        创建种植批次
POST /api/v1/batches/{id}/activities        农事记录（支持数组批量，client_uuid 幂等）
POST /api/v1/batches/{id}/inspection        上传检测结果
POST /api/v1/batches/{id}/codes             生成溯源码（返回数量与短码列表）
GET  /api/v1/trace/{code}                   公开溯源查询（无需鉴权，限流）
GET  /api/v1/trace/{code}/qrcode            返回二维码 PNG（带缓存头）

# 手工台账（staging，导入后不直接写 activity）
POST /api/v1/ledger/import                  批量导入手工台账（(ledger_batch, source_ref) 幂等）
GET  /api/v1/ledger?farm_id=                待合并台账列表（逐行返回 parsed/未认出的地块）

# 采集端 × 台账 对账合并
POST /api/v1/merges                         按当前待合并数据建工作单（只对账，不落地）
GET  /api/v1/merges                         工作单列表
GET  /api/v1/merges/{id}                    工作单详情：计数 + 恒等式核对 + 逐条明细（含两边取值与字段 diff、预选胜方）
POST /api/v1/merges/{id}/items/{itemId}/resolve   人工取舍（winner=device|ledger；仅台账项必须带 target_batch_id）
POST /api/v1/merges/{id}/items/{itemId}/skip      核销（重复抄录/作废，不进最终台账）
POST /api/v1/merges/{id}/items/{itemId}/unresolve 撤销裁决，回去重挑（改判也留档）
POST /api/v1/merges/{id}/apply              全部处理完后落地成「一份」（事务，对不上自动回滚）
POST /api/v1/merges/{id}/cancel             取消工作单，释放两边记录供重新合并
GET  /api/v1/merges/{id}/audit              人工操作留档（谁、何时、对哪条做了什么取舍）
```

## 7. 数据模型
```sql
farm(id, name, region_code, contact_ref, cert_no)
plot(id, farm_id, name, area_mu, geojson /* 简化多边形 */, soil_type)
crop_batch(id, plot_id, crop_id, sowing_date, harvest_date, expected_yield_kg, status /* growing|harvested|locked */)
activity(id, batch_id, client_uuid UNIQUE, kind /* fertilize|pesticide|irrigation|weed */, happened_at,
         input_id, dose, dose_unit, operator, photos jsonb, geo, created_at)
input_material(id, name, type, registration_no, safe_interval_days, active_ingredient)
inspection(id, batch_id, lab, sampled_at, result /* pass|fail */, report_url, items jsonb)
trace_code(id, batch_id, code UNIQUE, seq, printed_at, first_scanned_at, first_scan_region)

-- 合并（见 migrations/002_merge.sql）
activity(..., source /* device|ledger */, merge_run_id, merge_state /* kept|skipped */, note)
ledger_record(id, ledger_batch, source_ref, plot_name, plot_id, crop_id, kind, happened_on /* 台账按日 */,
              operator_norm/operator_raw, input_name, input_id, dose, dose_unit,
              has_evidence, evidence_ref, raw jsonb, status /* pending|merged|skipped|conflict */,
              merged_batch_id, merged_activity_id, merge_run_id)
merge_run(id, farm_id, status /* open|resolved|applied|cancelled */, device_in, ledger_in,
          device_only, ledger_only, matched_pairs, consistent, conflicts, resolved, unresolved,
          skipped, output_count, created_by, applied_at)
merge_item(id, run_id, match_type /* matched|device_only|ledger_only */, status /* consistent|conflict|pending|skipped */,
           地块/作物/日期/操作人, activity_id, ledger_id, winner_side, chosen_activity_id,
           new_input_id /* 人工改写 */, resolution_note, resolved_by/at, applied_activity_id, differences jsonb)
merge_audit_log(id, run_id, item_id, action /* create|resolve|skip|unresolve|apply|cancel */, actor, detail jsonb)
```

## 8. 关键实现点
- **幂等补报**：`activity.client_uuid` 唯一索引 + `ON CONFLICT DO NOTHING`，离线重传不会产生重复记录。
- **短码设计**：`Base32(时间戳低 32 位 + 批次序号 + CRC8)` 共 10 位，带校验位，扫码和手输都可。
- **时序完整性**：写入 activity 时校验 `happened_at` 不早于播种、不晚于采收，非法则拒绝（或标 `needs_review`）。
- **图片处理**：上传走预签名 URL 直传 MinIO，服务端只存 key；生成缩略图用于扫码页。
- **公开接口防护**：`/trace/{code}` 按 IP 限流（Redis 令牌桶，如 30 次/分钟），并对返回体脱敏。
- **安全间隔期**：发码时 `SELECT max(happened_at)` 与 `harvest_date` 比较，不足则拒绝。

### 8.1 采集端 × 手工台账 对账合并

**认同一件事**（`internal/merge`，纯 Go、无 DB、有单测）：四要素 `地块 + 作物 + 当地日期 + 操作人`
全部归一化后相等即配对。归一化（`pkg/mergenorm`）处理全半角、括号、空白与称谓差异
（“3 号地（东）”=`3号地(东)`、“张 三”=`张三`、`RICE`=`rice`）；同组多条时优先同操作类型，
避免把同日同地的「施肥/打药」错配。台账只记到「日」，不与采集端的当天时刻直接比较。

- **保留早的**：先比农事发生的日；同一天比记录产生时间（采集端拍照上报 / 台账录入）。
- **凭据优先**：仅一方有凭据（采集端现场照片、台账签字单/报告编号）→ 自动预选凭据方；
  双方都有凭据 → 不预选，强制人工拍板；都没有 → 预选更早的那条。预选只是建议，一律可改判。
- **摆出不一致**：逐字段（操作类型 / 投入品 / 用量 / 单位）生成 diff，含两边取值、缺失标记、
  预选胜方与理由，`GET /merges/{id}` 直接给人挑。
- **仅台账有**：确认时必须指定落到哪个种植批次，apply 时以 `source='ledger'` 补录进 activity
  （台账只记到日，时刻落正午；照片位存台账行号与凭据编号）。
- **核销**：重复抄录/作废可 skip，不进最终台账。

**总数对得上**（任意时刻成立，工作单详情里的 `check` 给出算式与 `balanced`）：

```
device_in = matched_pairs + device_only        # 采集端一条不多一条不少
ledger_in = matched_pairs + ledger_only        # 台账同理
events    = resolved + unresolved              # 配对算一个事件，单边算一个
applied   : output_count = resolved - skipped  # 落地后「一份」条数
```

引擎建单前先在内存里自检恒等式并逐条核对两侧引用；apply 在单个事务里落地，
提交前用数据库实时计数再复核一遍，对不上整体回滚，错误信息指明是哪一侧/哪一类计数出了问题，
再按 `merge_item` 逐行（含 `activity_id` / `ledger_id` / `applied_activity_id`）定位到具体记录。

**留档**：`merge_audit_log` 记录 create / resolve / skip / unresolve / apply / cancel
（操作人、明细项、取舍、理由与时间）；台账原始行存在 `ledger_record.raw`，
裁决结果（胜方、人工改写的投入品、备注）落在 `merge_item`，不可抵赖也可回溯改判。

**防重复认领**：建单即把两侧记录标到该单（`activity.merge_run_id` /
`ledger_record.merge_run_id`），一条记录同一时间只属于一张单；cancel 释放，apply 后归档。

**测试**：`go test ./...`（引擎纯逻辑用例）；带真实 PostgreSQL 的端到端用例用构建标签门控：

```bash
export DB_TEST_DSN_HOST=/tmp DB_TEST_PORT=5432   # 任意可连的 PG16
go test -tags dbtest ./internal/service/ ./internal/handler/ -v
```

覆盖：配对/冲突/单边、凭据优先、留早、幂等导入、改判撤销、核销、apply 事务与计数复核、
重复 apply 拒绝、cancel 释放，以及一条完整 HTTP 链路（建批次→批量上报→台账导入→建单→裁决→落地→审计）。

## 9. 技术约束与性能
- 所有时间存 UTC，展示按 `region_code` 转换（农事日期以当地日期为准，避免跨零点算错一天）。
- 溯源查询必须扛住扫码高峰（如直播带货瞬间），QPS 目标 500，走 Redis 缓存 5 分钟 + 缓存穿透保护。
- 码生成 10 万条用 `COPY`/批量 insert，单批 1000 条，避免长事务。

## 10. 验收标准
- 离线 200 条农事记录恢复网络后重传 3 次，数据库记录数仍为 200。
- 单次生成 10 万溯源码 < 30s，无重复（唯一约束兜底）。
- 检测失败或间隔期不足的批次 100% 无法发码。
- 扫码接口 P99 < 80ms，缓存命中率 > 95%。
- **台账合并**：N 条采集端 + M 条台账建单后，`device_in = matched_pairs + device_only`、
  `ledger_in = matched_pairs + ledger_only` 恒成立；所有冲突处理完才能 apply，
  apply 后 `output_count = resolved - skipped`，少一条/多一条都整体回滚并指出差异项；
  每次人工取舍可在 `/merges/{id}/audit` 查到操作人与时间；重复 apply 被拒绝；
  cancel 后同批记录可重新建单。

## 11. 边界（刻意不做）
不做农产品在线交易/订单/结算，不做库存 ERP，不做物流跟踪——避开黑名单中的电商订单、仓库库存类系统。

## 12. 容器化与构建（Docker）

本项目交付**必须能通过 Docker 构建与运行**，验收以 `docker compose up` 后对容器发起真实 HTTP 请求的结果为准。

- **Dockerfile（多阶段，统一 Go 模板）**
  - `builder`：`golang:1.22-alpine`，先 `COPY go.mod go.sum` 再 `go mod download`，然后 `CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/app ./cmd/api`
  - `runtime`：`gcr.io/distroless/static-debian12:nonroot`，只放单个静态二进制（自带 ca-certificates 与 tzdata），**非 root**、无 shell
  - BuildKit cache mount（`/go/pkg/mod`、`/root/.cache/go-build`）→ 只改业务代码时增量构建 5~15s
  - 健康检查：distroless 无 shell，不能写 `curl`，用 `HEALTHCHECK CMD ["/app","health"]`
- **docker-compose.yml**（服务名 `cc-052`）
  - `api`：端口 `9052:8080`，`restart: unless-stopped`
  - `db`：`postgres:16-alpine`（如用 jsonb 存农事照片元数据足够），数据卷 `pgdata` 持久化
  - `cache`：`redis:7-alpine`（扫码查询缓存 + 令牌桶限流）
  - `minio`：`minio/minio`，用于农事照片与检测报告，数据卷 `miniodata` 持久化
- **对象存储易踩的坑**
  - 预签名 URL 里的 host 必须是**客户端能访问到的地址**，容器内是 `minio:9000`、容器外是 `localhost:9000` → 用独立配置项 `MINIO_PUBLIC_ENDPOINT`，不能用容器内地址拼 URL
  - 启动时自动创建 bucket（幂等），并设置最小必要权限
- **数据库迁移**：入口脚本 `migrate && start`，幂等；农事记录表建 `client_uuid` 唯一索引（离线补报幂等的关键）
- **定时任务单实例**：间隔期校验与批量发码的定时任务**只允许一个实例执行**（Redis 分布式锁或 `replicas: 1`），否则会重复发通知
- **健康检查**：`HEALTHCHECK` → `GET /healthz`（含 db / cache / minio 状态）
- **日志与配置**：stdout 结构化日志；密钥走 `.env` + `secrets`，禁止硬编码 DB / MinIO 凭据

```bash
cd cc-052/cc-052
cp .env.example .env            # 填 DB / Redis / MinIO 与对外访问地址
docker compose up -d --build     # 起 api + db + cache + minio
curl http://localhost:9052/healthz
docker compose logs -f api
docker compose down              # 加 -v 一并清数据卷
```

- **验收**：容器内跑通「创建批次 → 离线补报 200 条并重传 3 次 → 生成 10 万溯源码 → 扫码查询」全链路，记录数与第 10 节一致；照片在容器外浏览器可直接显示（验证预签名 URL 地址正确）；重启后数据与文件仍在。

### 忽略文件（.gitignore / .dockerignore）

交付时必须**同时**提供 `.dockerignore` 与 `.gitignore`，两者作用不同、缺一不可（`.gitignore` 对 `docker build` 无效，反之亦然）。

- **`.dockerignore`**（决定构建上下文）
  ```
  bin
  tmp
  .cache
  coverage.out
  .git
  .gitignore
  .env
  .env.*
  *.log
  coverage
  .vscode
  .idea
  Dockerfile
  docker-compose.yml
  README.md
  ```
  - **不要**忽略 `go.mod` / `go.sum` 与 `migrations/`、`*.sql`（依赖解析与入口 `migrate` 都需要）
  - 投入品字典等基础数据脚本（`seed/*`）必须保留
  - **保留** `.env.example`，只忽略真实 `.env`
  - 本地测试用的模拟农事照片（`testdata/photos/`）建议忽略，避免把大文件写进仓库与镜像
- **`.gitignore`**
  ```
  bin/
  tmp/
  coverage.out
  *.test
  .env
  .env.local
  *.log
  coverage/
  .DS_Store
  .vscode/
  .idea/
  testdata/photos/
  *.mp4
  ```
- **安全自检**：`docker history <img>` 无密钥；`git status` 不出现 `.env` 与大体积测试素材；MinIO 凭据只在运行时注入
