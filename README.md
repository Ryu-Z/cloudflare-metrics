# cloudflare-analytics-metrics-exporter

一个 Prometheus Exporter，用于采集 Cloudflare 用量指标并通过 `/metrics` 暴露，覆盖：

- zone 级别 HTTP Analytics
- zone 级别 Spectrum Analytics
- 基于已配置 zones 的 account 汇总视图
- 每日完整数据和本月累计数据

采集模式已经调整为“上游按计划拉取一次，结果缓存在内存中”：

- 启动时会先预热一次缓存
- 之后按计划拉取一次 Cloudflare 数据，默认每 30 分钟刷新一次内存缓存
- Prometheus 抓取 `/metrics` 时只读取内存缓存，不会触发新的 Cloudflare 请求
- 如果采集失败，会按配置重试；连续失败达到阈值后发送 Lark 机器人告警

当前版本重点暴露 `bytes`、`requests` 和 Spectrum ingress/egress，适合做总体流量、成本趋势和预算告警。对于 Spectrum，`bytes_total` 按 `bytesIngress + bytesEgress` 计算。

## 指标

Exporter 会输出以下核心指标：

- `cloudflare_bytes_total_today`
- `cloudflare_bytes_ingress_today`
- `cloudflare_bytes_egress_today`
- `cloudflare_bytes_cached_today`
- `cloudflare_requests_total_today`
- `cloudflare_bytes_total_daily`
- `cloudflare_bytes_ingress_daily`
- `cloudflare_bytes_egress_daily`
- `cloudflare_bytes_cached_daily`
- `cloudflare_requests_total_daily`
- `cloudflare_bytes_total_monthly`
- `cloudflare_bytes_ingress_monthly`
- `cloudflare_bytes_egress_monthly`
- `cloudflare_bytes_cached_monthly`
- `cloudflare_requests_total_monthly`
- `cloudflare_bytes_total_last_month`
- `cloudflare_bytes_ingress_last_month`
- `cloudflare_bytes_egress_last_month`
- `cloudflare_bytes_cached_last_month`
- `cloudflare_requests_total_last_month`
- `cloudflare_bytes_total_closed_month`
- `cloudflare_bytes_ingress_closed_month`
- `cloudflare_bytes_egress_closed_month`
- `cloudflare_bytes_cached_closed_month`
- `cloudflare_requests_total_closed_month`
- `cloudflare_bytes_total_last_month_to_date`
- `cloudflare_bytes_ingress_last_month_to_date`
- `cloudflare_bytes_egress_last_month_to_date`
- `cloudflare_bytes_cached_last_month_to_date`
- `cloudflare_requests_total_last_month_to_date`
- `cloudflare_analytics_query_total`
- `cloudflare_analytics_last_success_timestamp`
- `cloudflare_analytics_up`

所有用量指标都带有这些标签：

- `scope`: `zone` 或 `account`
- `account_id`
- `zone_id`
- `zone_name`
- `zone_domain`
- `product`: `all` / `http` / `spectrum`

其中 `zone_id` 默认表示主查询 zone。若配置了单独的 `spectrum_zone_id`，Spectrum 上游查询会使用该值，但指标标签仍沿用当前条目的 `zone_id`、`zone_name`、`zone_domain` 作为业务归属标识。

不再使用 `date` / `month` 这类时间标签。时间范围由 exporter 查询逻辑决定：

- `*_today`: 当日从 00:00 UTC 到当前时刻的累计
- `*_daily`: 昨日完整自然日
- `*_monthly`: 本月 1 日到当前时刻的累计
- `*_last_month`: 上一个完整自然月
- `*_closed_month`: 以当前调度时区为准，最近一个已经结束的完整自然月；查询窗口按 UTC 月边界对齐，适合和 Cloudflare 月封账接口直接比对
- `*_last_month_to_date`: 上个月从 1 日到“与今天对应的自然日”为止的累计，若上月天数不足则自动截到月底

这样 Prometheus 不会因为月份标签变化而持续创建新时序，Grafana 做日趋势、月累计和月环比也更直接。

`zone_domain` 会作为指标标签暴露；文档中的配置和查询示例统一使用占位域名 `example.com`。

## 配置

参考 [`config.yaml.example`](/Users/ryuliu/codex/cloudflare-metrics/config.yaml.example)：

```yaml
cloudflare:
  api_token: ""
  account_id: ""
  zones:
    - zone_id: ""
      spectrum_zone_id: ""
      name: ""
      domain: ""

metrics:
  listen_addr: ":9589"

schedule:
  timezone: "Asia/Shanghai"
  interval_minutes: 30
  daily:
    hour: 1
    minute: 5
  retry:
    max_attempts: 5
    delay_seconds: 300

alerting:
  lark:
    webhook_url: ""
    secret: ""
    title: "Cloudflare Analytics Exporter Alert"
    dashboard_url: ""
    mention_user_ids: "ou_xxxxxxxxxx,ou_yyyyyyyyyy"
```

配置说明：

- `schedule.interval_minutes`: 固定间隔轮询，单位分钟；大于 0 时优先使用，例如 `30` 表示每半小时刷新一次内存缓存
- `schedule.daily`: 兼容旧版的按天定时执行；仅当 `interval_minutes <= 0` 时才会生效
- `schedule.timezone`: 只影响任务调度在本地何时执行，不影响 Cloudflare 查询窗口
- `schedule.retry.max_attempts`: 单次计划任务失败后的最大重试次数
- `schedule.retry.delay_seconds`: 每次重试之间的等待秒数
- `alerting.lark.webhook_url`: Lark 自定义机器人 webhook 地址，留空则不发送告警
- `alerting.lark.secret`: 机器人开启签名校验时填写
- `alerting.lark.title`: 告警标题
- `alerting.lark.dashboard_url`: 可选，告警里附带的 Grafana 看板链接
- `alerting.lark.mention_user_ids`: 可选，逗号分隔的 Lark `user_id`，告警会在富文本消息中 `@` 这些人
- 应用启动时会先读取与 `config.yaml` 同目录下的 `.env`
- 如果 `.env` 与 `config.yaml` 同时存在，`.env` 的值优先级更高

推荐把敏感信息放到 `.env`：

- `CF_API_TOKEN`
- `CF_ACCOUNT_ID`
- `CF_ZONE_ID`
- `CF_SPECTRUM_ZONE_ID`
- `CF_ZONE_NAME`
- `CF_ZONE_DOMAIN`
- `LARK_WEBHOOK_URL`
- `LARK_SECRET`
- `LARK_MENTION_USER_IDS`

如果你的 HTTP Analytics 和 Spectrum Analytics 不是同一个 zone，可以这样配置：

```yaml
cloudflare:
  zones:
    - zone_id: "http-zone-id"
      spectrum_zone_id: "spectrum-zone-id"
      name: "example-zone"
      domain: "example.com"
```

或在 `.env` 中设置：

```bash
CF_ZONE_ID=http-zone-id
CF_SPECTRUM_ZONE_ID=spectrum-zone-id
```

当前 Spectrum 查询会使用你提供的这类接口：

```text
GET /client/v4/zones/<spectrum_zone_id>/spectrum/analytics/events/bytime?metrics=bytesIngress,bytesEgress&time_delta=day&limit=100000&since=...&until=...
```

并使用返回体中的 `totals.bytesIngress + totals.bytesEgress` 作为 Spectrum 的 `bytes_total`。

为避免时区偏移导致日/月总量不一致，Exporter 的 Cloudflare 查询窗口统一按 `UTC` 计算；`schedule.timezone` 仅用于决定本地什么时候触发采集任务。

换句话说：

- Cloudflare 上游查询时间窗：统一使用 `UTC`
- 容器里的 `TZ` / `schedule.timezone`：只影响日志显示时间、每日任务在本地几点执行、以及“最近完整月”按哪个本地日期切换

因此这里配置时区不是为了让 Cloudflare API 用本地时区查询，而是为了让 exporter 在本地运维视角下更可控、更容易排障。

`.env` 示例请参考 [`.env.example`](/Users/ryuliu/codex/cloudflare-metrics/.env.example)。

配置文件说明：

- [`config.yaml`](/Users/ryuliu/codex/cloudflare-metrics/config.yaml): 正式运行配置，给真实 exporter 使用
- [`config.verify-failure.yaml`](/Users/ryuliu/codex/cloudflare-metrics/config.verify-failure.yaml): 告警演练配置，主要用于验证重试和 Lark 通知链路

两者的主要区别：

- `config.yaml` 使用真实 Cloudflare token，采集应该成功
- `config.verify-failure.yaml` 通常配合临时坏 token 覆盖使用，让采集失败，从而触发重试和告警
- `config.verify-failure.yaml` 的 `retry.delay_seconds` 一般会设得更短，方便快速演练

调度配置示例：

```yaml
schedule:
  timezone: "Asia/Shanghai"
  interval_minutes: 30
  daily:
    hour: 1
    minute: 5
  retry:
    max_attempts: 5
    delay_seconds: 300
```

含义：

- `timezone`: 调度执行使用的时区
- `interval_minutes`: 每隔多少分钟刷新一次内存缓存，这里表示每 `30` 分钟采集一次
- `daily.hour` / `daily.minute`: 仅在 `interval_minutes <= 0` 时使用，这里表示每天 `01:05`
- `retry.max_attempts`: 当次采集失败后最多重试几次
- `retry.delay_seconds`: 每次重试之间等待多少秒

## 运行

本地运行：

```bash
go run . -config config.yaml
```

Docker 运行：

```bash
docker build -t cloudflare-analytics-metrics-exporter .
docker run -d --name cf-analytics-exporter \
  -v $(pwd)/config.yaml:/app/config.yaml:ro \
  -p 9589:9589 \
  --restart unless-stopped \
  cloudflare-analytics-metrics-exporter
```

容器运行说明：

- 运行镜像时会创建并切换到非 root 的 `app` 用户
- exporter 进程默认以 `app` 用户身份运行

Docker Compose 运行：

仓库已包含 [`docker-compose.yaml`](/Users/ryuliu/codex/cloudflare-metrics/docker-compose.yaml)，默认会：

- 构建当前目录下的镜像
- 将宿主机的配置文件挂载到容器 `/app/config.yaml`
- 暴露可配置端口，默认 `9589:9589`
- 设置 `restart: unless-stopped`
- 通过 `.env` 读取容器名、时区、端口和配置文件路径

先准备 `.env`：

```bash
cp .env.example .env
```

默认的 [`.env.example`](/Users/ryuliu/codex/cloudflare-metrics/.env.example) 中定义了：

- `CONTAINER_NAME`
- `HOST_PORT`
- `CONTAINER_PORT`
- `TZ`
- `CONFIG_PATH`
- `CF_API_TOKEN`
- `CF_ACCOUNT_ID`
- `CF_ZONE_ID`
- `CF_SPECTRUM_ZONE_ID`
- `CF_ZONE_NAME`
- `CF_ZONE_DOMAIN`
- `LARK_WEBHOOK_URL`
- `LARK_SECRET`
- `LARK_TITLE`
- `LARK_DASHBOARD_URL`
- `LARK_MENTION_USER_IDS`

启动：

```bash
docker compose up -d --build
```

说明：

- `docker-compose.yaml` 同时包含 `build` 和 `image`
- 执行 `docker compose up -d --build` 时，会优先使用当前工作区代码重新构建镜像
- 如果只执行 `docker compose up -d`，可能继续使用本地已有镜像或远端已拉取镜像
- 当前运行层使用 `debian:bookworm-slim`
- 镜像中已安装 `tzdata`，程序本身也内置了 Go `time/tzdata`，可正常识别 `Asia/Shanghai`
- 这里不需要挂载宿主机的 `/etc/localtime` 或 `/usr/share/zoneinfo`
- `TZ` 只是容器内的时区环境变量，用于日志时间和调度触发时间

查看日志：

```bash
docker compose logs -f
```

停止：

```bash
docker compose down
```

如果你需要覆盖默认配置，可以参考 [`docker-compose.override.yaml.example`](/Users/ryuliu/codex/cloudflare-metrics/docker-compose.override.yaml.example)：

- 配合 `.env` 修改宿主机映射端口
- 补充环境变量
- 调整配置文件挂载路径

使用方式：

```bash
cp docker-compose.override.yaml.example docker-compose.override.yaml
```

然后按需编辑 `docker-compose.override.yaml`，之后直接执行：

```bash
docker compose up -d --build
```

`docker compose` 会自动合并 `docker-compose.yaml` 和 `docker-compose.override.yaml`。

Makefile 快捷命令：

仓库已包含 [`Makefile`](/Users/ryuliu/codex/cloudflare-metrics/Makefile)，常用命令如下：

```bash
make fmt
make lint
make build
make run
make up
make logs
make ps
make down
make restart
make test-alert
```

说明：

- `make fmt`: 执行 `gofmt`
- `make lint`: 执行基础 Go 检查（当前为 `go test ./...`）
- `make build`: 本地编译二进制
- `make run`: 使用 `config.yaml` 本地运行
- `make up`: 使用 Docker Compose 启动服务
- `make logs`: 查看 Docker Compose 日志
- `make ps`: 查看容器状态
- `make down`: 停止 Docker Compose
- `make restart`: 重建并重启 Docker Compose
- `make test-alert`: 使用 `config.verify-failure.yaml`，并通过临时坏 token 覆盖 `.env`，触发失败告警演练

运行行为说明：

- 进程启动后会先执行一次采集来填充缓存
- 后续只在每日计划时间向 Cloudflare 拉取数据
- 如果重试全部失败，`/metrics` 仍会继续暴露上一次成功的缓存
- 如果最近一次采集失败，`/healthz` 会返回 `503`
- Lark 告警会包含失败 zone、失败阶段、最近一次成功时间和可选 dashboard 链接
- Lark 告警使用富文本 `post` 格式，支持标题、链接和 `@user_id`

## 数据来源

- HTTP 用量来自 Cloudflare GraphQL Analytics API
- Spectrum 用量来自 Cloudflare Spectrum Analytics Summary API

之所以这样组合，是因为 Cloudflare 官方文档明确给出了 GraphQL 的 HTTP analytics 查询方式，以及 Spectrum 独立 analytics summary endpoint 的官方接口定义。

## 月份天数处理

不同月份天数已经按自然月边界处理：

- 本月累计从当月 1 日 00:00:00 开始
- 上月总量从上月 1 日开始，到本月 1 日前一秒结束
- 上月同期累计会取“上个月对应日期”的同一时刻；如果本月是 30/31 日而上月没有这一天，则自动使用上月最后一天
- 月度调度如果配置了 `day: 31`，遇到 2 月会自动落到 2 月最后一天，而不是报错

## Grafana

仓库里已经提供可导入的 Grafana 看板：

- [`grafana/dashboards/cloudflare-analytics-overview.json`](/Users/ryuliu/codex/cloudflare-metrics/grafana/dashboards/cloudflare-analytics-overview.json)
- [`grafana/dashboards/cloudflare-spectrum-cost-overview.json`](/Users/ryuliu/codex/cloudflare-metrics/grafana/dashboards/cloudflare-spectrum-cost-overview.json)

建议在 Grafana 中把 Prometheus 数据源命名为 `Prometheus`，然后直接导入对应 JSON。

使用建议：

- `cloudflare-analytics-overview.json`: 通用总览，看板默认打开为 Spectrum 视角，但仍可切换到 `http` / `all`
- `cloudflare-spectrum-cost-overview.json`: 运营/成本对账视角，聚焦 Spectrum 的日流量、月封账、环比和采集健康状态

看板包含这些视图：

- 今日累计流量
- 今日累计请求数
- 昨日完整流量
- 本月累计流量
- 最近完整月流量
- 上月同期累计流量
- 今日累计流量趋势
- 今日累计请求趋势
- 本月累计 vs 最近完整月
- 日环比百分比
- 月对比变化率

如果你的目标是做 Spectrum 成本核对，建议优先导入 `cloudflare-spectrum-cost-overview.json`。

关键 PromQL 示例：

如果你的目标是做 Spectrum 用量、成本估算和月结核对，建议优先使用 `product="spectrum"`。

```promql
cloudflare_bytes_total_daily{scope="zone", zone_domain="example.com", product="spectrum"}
```

```promql
cloudflare_bytes_total_monthly{scope="zone", zone_domain="example.com", product="spectrum"}
```

```promql
cloudflare_bytes_total_closed_month{scope="zone", zone_domain="example.com", product="spectrum"}
```

```promql
cloudflare_bytes_total_last_month_to_date{scope="zone", zone_domain="example.com", product="spectrum"}
```

```promql
(
  cloudflare_bytes_total_daily{scope="zone", zone_domain="example.com", product="spectrum"}
  -
  cloudflare_bytes_total_daily{scope="zone", zone_domain="example.com", product="spectrum"} offset 1d
)
/
clamp_min(
  cloudflare_bytes_total_daily{scope="zone", zone_domain="example.com", product="spectrum"} offset 1d,
  1
) * 100
```

```promql
(
  cloudflare_bytes_total_monthly{scope="zone", zone_domain="example.com", product="spectrum"}
  -
  cloudflare_bytes_total_closed_month{scope="zone", zone_domain="example.com", product="spectrum"}
)
/
clamp_min(
  cloudflare_bytes_total_closed_month{scope="zone", zone_domain="example.com", product="spectrum"},
  1
) * 100
```

## Prometheus 告警规则

仓库里提供了一份可直接修改后使用的 Spectrum 告警规则：

- [`prometheus/rules/cloudflare-spectrum-alerts.yaml`](/Users/ryuliu/codex/cloudflare-metrics/prometheus/rules/cloudflare-spectrum-alerts.yaml)

当前示例规则覆盖：

- `CloudflareAnalyticsCollectionFailed`: exporter 连续 10 分钟没有有效快照
- `CloudflareSpectrumDailyTrafficSpike`: 昨日 Spectrum 流量较前一天增长超过 50%，且绝对值超过 500 GB
- `CloudflareSpectrumMonthlyBudgetWarning`: 本月 Spectrum 累计流量超过 5 TB
- `CloudflareSpectrumMonthlyBudgetExceeded`: 本月 Spectrum 累计流量超过 10 TB

接入 Prometheus 示例：

```yaml
rule_files:
  - /etc/prometheus/rules/cloudflare-spectrum-alerts.yaml
```

如果你用的是 Docker / Kubernetes，请把规则文件挂载到 Prometheus 容器或 ConfigMap 中，然后 reload Prometheus。

Kubernetes 示例文件：

- [`k8s/cloudflare-spectrum-alerts-configmap.yaml`](/Users/ryuliu/codex/cloudflare-metrics/k8s/cloudflare-spectrum-alerts-configmap.yaml): 适合原生 Prometheus，通过 ConfigMap 挂载规则文件
- [`k8s/cloudflare-spectrum-alerts-prometheusrule.yaml`](/Users/ryuliu/codex/cloudflare-metrics/k8s/cloudflare-spectrum-alerts-prometheusrule.yaml): 适合 Prometheus Operator / kube-prometheus-stack

原生 Prometheus 落地步骤：

风险提示：修改规则文件后，Prometheus reload 前不会生效；若挂载路径或 `rule_files` 配置错误，可能导致新规则不加载。

```bash
kubectl -n monitoring apply -f k8s/cloudflare-spectrum-alerts-configmap.yaml
kubectl -n monitoring rollout restart deploy/prometheus
```

Prometheus Operator 落地步骤：

风险提示：请先确认集群里 CRD `prometheusrules.monitoring.coreos.com` 已安装，且当前 namespace 被对应 Prometheus 实例的 rule selector 选中。

```bash
kubectl -n monitoring apply -f k8s/cloudflare-spectrum-alerts-prometheusrule.yaml
```

验证命令：

```bash
kubectl -n monitoring get prometheusrule cloudflare-spectrum-alerts
kubectl -n monitoring get configmap cloudflare-spectrum-alerts
```

如果你使用 kube-prometheus-stack，还可以进一步检查：

```bash
kubectl -n monitoring describe prometheusrule cloudflare-spectrum-alerts
```

建议上线前至少调整这两个阈值：

- `CloudflareSpectrumMonthlyBudgetWarning`
- `CloudflareSpectrumMonthlyBudgetExceeded`

默认阈值只是示例值，实际应该结合你的：

- 月预算
- 历史最近完整月流量
- 业务波峰
- Cloudflare Spectrum 计费方式

验证命令：

```bash
promtool check rules prometheus/rules/cloudflare-spectrum-alerts.yaml
```

回滚方案：

```bash
kubectl -n monitoring delete -f k8s/cloudflare-spectrum-alerts-prometheusrule.yaml
kubectl -n monitoring delete -f k8s/cloudflare-spectrum-alerts-configmap.yaml
```
