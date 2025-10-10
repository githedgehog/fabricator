## Hotfix: Add OrgID Injection to Loki Gateway

If you see “no org id” or 401 errors in Grafana/Loki, your gateway Nginx config is missing OrgID injection logic.

### 1. Edit the ConfigMap

```sh
kubectl -n lgtm edit configmap loki-gateway
```
Replace the entire value of `nginx.conf:` with the fixed config below.

---

### Current (Problematic) nginx.conf

```nginx
worker_processes  5;  ## Default: 1
error_log  /dev/stderr;
pid        /tmp/nginx.pid;
worker_rlimit_nofile 8192;

events {
  worker_connections  4096;  ## Default: 1024
}

http {
  client_body_temp_path /tmp/client_temp;
  proxy_temp_path       /tmp/proxy_temp_path;
  fastcgi_temp_path     /tmp/fastcgi_temp;
  uwsgi_temp_path       /tmp/uwsgi_temp;
  scgi_temp_path        /tmp/scgi_temp;

  client_max_body_size  4M;

  proxy_read_timeout    600; ## 10 minutes
  proxy_send_timeout    600;
  proxy_connect_timeout 600;

  proxy_http_version    1.1;

  default_type application/octet-stream;
  log_format   main '$remote_addr - $remote_user [$time_local]  $status '
        '"$request" $body_bytes_sent "$http_referer" '
        '"$http_user_agent" "$http_x_forwarded_for"';
  access_log   /dev/stderr  main;

  sendfile     on;
  tcp_nopush   on;
  resolver kube-dns.kube-system.svc.cluster.local.;

  # if the X-Query-Tags header is empty, set a noop= without a value as empty values are not logged
  map $http_x_query_tags $query_tags {
    ""        "noop=";            # When header is empty, set noop=
    default   $http_x_query_tags; # Otherwise, preserve the original value
  }

  server {
    listen             8080;
    listen             [::]:8080;

    location = / {
      return 200 'OK';
      auth_basic off;
    }

    ########################################################
    # Configure backend targets
    location ^~ /ui {
      proxy_pass      http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # Distributor
    location = /api/prom/push {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /loki/api/v1/push {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /distributor/ring {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /otlp/v1/logs {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # Ingester
    location = /flush {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location ^~ /ingester/ {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /ingester {
      internal;        # to suppress 301
    }

    # Ring
    location = /ring {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # MemberListKV
    location = /memberlist {
      proxy_pass      http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # Ruler
    location = /ruler/ring {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /api/prom/rules {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location ^~ /api/prom/rules/ {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /loki/api/v1/rules {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location ^~ /loki/api/v1/rules/ {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /prometheus/api/v1/alerts {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /prometheus/api/v1/rules {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # Compactor
    location = /compactor/ring {
      proxy_pass      http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /loki/api/v1/delete {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /loki/api/v1/cache/generation_numbers {
      proxy_pass      http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # IndexGateway
    location = /indexgateway/ring {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # QueryScheduler
    location = /scheduler/ring {
      proxy_pass      http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # Config
    location = /config {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # QueryFrontend, Querier
    location = /api/prom/tail {
      proxy_set_header Upgrade $http_upgrade;
      proxy_set_header Connection "upgrade";
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /loki/api/v1/tail {
      proxy_set_header Upgrade $http_upgrade;
      proxy_set_header Connection "upgrade";
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location ^~ /api/prom/ {
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /api/prom {
      internal;        # to suppress 301
    }
    location ^~ /loki/api/v1/ {
      proxy_set_header X-Query-Tags "${query_tags},user=${http_x_grafana_user},dashboard_id=${http_x_dashboard_uid},dashboard_title=${http_x_dashboard_title},panel_id=${http_x_panel_id},panel_title=${http_x_panel_title},source_rule_uid=${http_x_rule_uid},rule_name=${http_x_rule_name},rule_folder=${http_x_rule_folder},rule_version=${http_x_rule_version},rule_source=${http_x_rule_source},rule_type=${http_x_rule_type}";
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /loki/api/v1 {
      internal;        # to suppress 301
    }
  }
}
```

---

### Fixed nginx.conf (with OrgID injection)

```nginx
worker_processes  5;  ## Default: 1
error_log  /dev/stderr;
pid        /tmp/nginx.pid;
worker_rlimit_nofile 8192;

events {
  worker_connections  4096;  ## Default: 1024
}

http {
  client_body_temp_path /tmp/client_temp;
  proxy_temp_path       /tmp/proxy_temp_path;
  fastcgi_temp_path     /tmp/fastcgi_temp;
  uwsgi_temp_path       /tmp/uwsgi_temp;
  scgi_temp_path        /tmp/scgi_temp;

  client_max_body_size  4M;

  proxy_read_timeout    600; ## 10 minutes
  proxy_send_timeout    600;
  proxy_connect_timeout 600;

  proxy_http_version    1.1;

  default_type application/octet-stream;
  log_format   main '$remote_addr - $remote_user [$time_local]  $status '
        '"$request" $body_bytes_sent "$http_referer" '
        '"$http_user_agent" "$http_x_forwarded_for"';
  access_log   /dev/stderr  main;

  sendfile     on;
  tcp_nopush   on;
  resolver kube-dns.kube-system.svc.cluster.local.;

  map $http_x_query_tags $query_tags {
    ""        "noop=";
    default   $http_x_query_tags;
  }

  server {
    listen             8080;
    listen             [::]:8080;

    # Always set OrgID header to 'anonymous' if not present
    set $orgid $http_x_scope_orgid;
    if ($orgid = "") {
      set $orgid "anonymous";
    }

    location = / {
      return 200 'OK';
      auth_basic off;
    }

    location ^~ /ui {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass      http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # Distributor
    location = /api/prom/push {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /loki/api/v1/push {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /distributor/ring {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /otlp/v1/logs {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # Ingester
    location = /flush {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location ^~ /ingester/ {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /ingester {
      internal;        # to suppress 301
    }

    # Ring
    location = /ring {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # MemberListKV
    location = /memberlist {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass      http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # Ruler
    location = /ruler/ring {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /api/prom/rules {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location ^~ /api/prom/rules/ {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /loki/api/v1/rules {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location ^~ /loki/api/v1/rules/ {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /prometheus/api/v1/alerts {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /prometheus/api/v1/rules {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # Compactor
    location = /compactor/ring {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass      http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /loki/api/v1/delete {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /loki/api/v1/cache/generation_numbers {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass      http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # IndexGateway
    location = /indexgateway/ring {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # QueryScheduler
    location = /scheduler/ring {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass      http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # Config
    location = /config {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }

    # QueryFrontend, Querier
    location = /api/prom/tail {
      proxy_set_header Upgrade $http_upgrade;
      proxy_set_header Connection "upgrade";
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /loki/api/v1/tail {
      proxy_set_header Upgrade $http_upgrade;
      proxy_set_header Connection "upgrade";
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location ^~ /api/prom/ {
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /api/prom {
      internal;        # to suppress 301
    }
    location ^~ /loki/api/v1/ {
      proxy_set_header X-Query-Tags "${query_tags},user=${http_x_grafana_user},dashboard_id=${http_x_dashboard_uid},dashboard_title=${http_x_dashboard_title},panel_id=${http_x_panel_id},panel_title=${http_x_panel_title},source_rule_uid=${http_x_rule_uid},rule_name=${http_x_rule_name},rule_folder=${http_x_rule_folder},rule_version=${http_x_rule_version},rule_source=${http_x_rule_source},rule_type=${http_x_rule_type}";
      proxy_set_header X-Scope-OrgID $orgid;
      proxy_pass       http://loki.lgtm.svc.cluster.local:3100$request_uri;
    }
    location = /loki/api/v1 {
      internal;        # to suppress 301
    }
  }
}
```

---

### 2. Restart the Gateway Pod

```sh
kubectl -n lgtm delete pod -l app.kubernetes.io/name=loki-gateway
```

---

After restart, your gateway will inject OrgID and the error will be resolved.