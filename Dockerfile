FROM golang:1.22 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/cloudflare-analytics-metrics-exporter .

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system app \
    && useradd --system --gid app --home-dir /app --create-home app
WORKDIR /app
COPY --from=build /out/cloudflare-analytics-metrics-exporter /app/cloudflare-analytics-metrics-exporter
RUN chown -R app:app /app
USER app
EXPOSE 9589
ENTRYPOINT ["/app/cloudflare-analytics-metrics-exporter"]
CMD ["-config", "/app/config.yaml"]
