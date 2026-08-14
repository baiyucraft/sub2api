#!/usr/bin/env bash
set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
scripts_root=$(cd -- "$script_dir/../.." && pwd -P)
source "$scripts_root/maintenance/release/compose-contract.sh"

image_id="sha256:$(printf 'a%.0s' {1..64})"
instance_id=test-release-active
real_docker=$(command -v docker)
[[ -n $real_docker ]]
"$real_docker" compose version >/dev/null
tmp=$(mktemp -d)
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

render_compose_contract() {
  local mode=${1:?mode is required}
  local host_port=${2:?host port is required}
  local render_dir="$tmp/render-$mode-$host_port"
  local base="$render_dir/base.yml"
  local override="$render_dir/override.yml"
  install -d -m 700 "$render_dir"
  if [[ $mode == host ]]; then
    cat > "$base" <<'EOF'
services:
  sub2api:
    image: old
    container_name: sub2api
    network_mode: host
    environment:
      SERVER_HOST: 127.0.0.1
      SERVER_PORT: "${SERVER_PORT:-18080}"
    healthcheck:
      test: ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", "http://127.0.0.1:${SERVER_PORT:-18080}/health"]
EOF
  else
    cat > "$base" <<'EOF'
services:
  sub2api:
    image: old
    container_name: sub2api
    ports:
      - "${BIND_HOST:-0.0.0.0}:${SERVER_PORT:-8080}:8080"
    environment:
      SERVER_HOST: 0.0.0.0
      SERVER_PORT: "8080"
    healthcheck:
      test: ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", "http://localhost:8080/health"]
EOF
  fi
  write_release_active_override "$override" "$image_id" "$instance_id" "$host_port" "$mode"
  BIND_HOST=127.0.0.1 SERVER_PORT="$host_port" \
    "$real_docker" compose -f "$base" -f "$override" config --format json
}

compose_json() {
  local mode=${1:?mode is required}
  local host_port=${2:?host port is required}
  local health_test=${3:?health test json is required}
  if [[ $mode == host ]]; then
    jq -cn --arg port "$host_port" --argjson health "$health_test" '{
      services: {sub2api: {
        container_name: "sub2api",
        image: "candidate",
        network_mode: "host",
        environment: {
          SERVER_HOST: "127.0.0.1",
          SERVER_PORT: $port,
          SUB2API_INSTANCE_ID: "test-release-active",
          SUB2API_BACKGROUND_ACTIVATION_FILE: "/app/data/.sub2api-active-instance"
        },
        healthcheck: {test: $health}
      }}
    }'
  else
    jq -cn --arg port "$host_port" --argjson health "$health_test" '{
      services: {sub2api: {
        container_name: "sub2api",
        image: "candidate",
        environment: {
          SERVER_HOST: "0.0.0.0",
          SERVER_PORT: "8080",
          SUB2API_INSTANCE_ID: "test-release-active",
          SUB2API_BACKGROUND_ACTIVATION_FILE: "/app/data/.sub2api-active-instance"
        },
        ports: [{target: 8080, published: $port, host_ip: "127.0.0.1"}],
        healthcheck: {test: $health}
      }}
    }'
  fi
}

runtime_json() {
  local mode=${1:?mode is required}
  local host_port=${2:?host port is required}
  local health_url=${3:?health url is required}
  if [[ $mode == host ]]; then
    jq -cn --arg image "$image_id" --arg port "$host_port" --arg health "$health_url" '[{
      Image: $image,
      Config: {
        Env: ["SERVER_HOST=127.0.0.1", ("SERVER_PORT=" + $port)],
        Healthcheck: {Test: ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", $health]}
      },
      HostConfig: {NetworkMode: "host"},
      NetworkSettings: {Ports: {}}
    }]'
  else
    jq -cn --arg image "$image_id" --arg port "$host_port" --arg health "$health_url" '[{
      Image: $image,
      Config: {
        Env: ["SERVER_HOST=0.0.0.0", "SERVER_PORT=8080"],
        Healthcheck: {Test: ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", $health]}
      },
      HostConfig: {NetworkMode: "sub2api-network"},
      NetworkSettings: {Ports: {"8080/tcp": [{HostIp: "127.0.0.1", HostPort: $port}]}}
    }]'
  fi
}

fake_bin="$tmp/bin"
install -d -m 700 "$fake_bin"
cat > "$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
[[ $1 == inspect ]]
cat -- "$FAKE_DOCKER_INSPECT"
EOF
chmod 700 "$fake_bin/docker"
export PATH="$fake_bin:$PATH"

for host_port in 18080 18081; do
  host_url="http://127.0.0.1:${host_port}/health"
  host_health=$(jq -cn --arg url "$host_url" '["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", $url]')
  host_compose=$(compose_json host "$host_port" "$host_health")
  [[ $(sub2api_compose_network_mode "$host_compose" "$host_port") == host ]]
  assert_sub2api_healthcheck_contract "$host_compose" host "$host_port"

  bridge_url=http://127.0.0.1:8080/health
  bridge_health=$(jq -cn --arg url "$bridge_url" '["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", $url]')
  bridge_compose=$(compose_json bridge "$host_port" "$bridge_health")
  [[ $(sub2api_compose_network_mode "$bridge_compose" "$host_port") == bridge ]]
  assert_sub2api_healthcheck_contract "$bridge_compose" bridge "$host_port"

  localhost_health='["CMD","wget","-q","-T","5","-O","/dev/null","http://localhost:8080/health"]'
  localhost_compose=$(compose_json bridge "$host_port" "$localhost_health")
  assert_sub2api_healthcheck_contract "$localhost_compose" bridge "$host_port"

  rendered_host=$(render_compose_contract host "$host_port")
  [[ $(sub2api_compose_network_mode "$rendered_host" "$host_port") == host ]]
  assert_sub2api_healthcheck_contract "$rendered_host" host "$host_port"
  jq -e --arg instance "$instance_id" '.services.sub2api.environment.SUB2API_INSTANCE_ID == $instance' <<<"$rendered_host" >/dev/null

  rendered_bridge=$(render_compose_contract bridge "$host_port")
  [[ $(sub2api_compose_network_mode "$rendered_bridge" "$host_port") == bridge ]]
  assert_sub2api_healthcheck_contract "$rendered_bridge" bridge "$host_port"
  jq -e --arg instance "$instance_id" '.services.sub2api.environment.SUB2API_INSTANCE_ID == $instance' <<<"$rendered_bridge" >/dev/null

  for invalid_health in \
    '["CMD-SHELL","wget -q -T 5 -O /dev/null http://127.0.0.1:8080/health"]' \
    '["CMD","wget","-q","-T","5","-O","/dev/null","http://127.0.0.1:8080/health","extra"]' \
    '["CMD","wget","-q","-T","5","-O","/dev/null","http://127.0.0.1:8080/health-bad"]'; do
    invalid_compose=$(compose_json bridge "$host_port" "$invalid_health")
    if assert_sub2api_healthcheck_contract "$invalid_compose" bridge "$host_port"; then
      exit 1
    fi
  done

  host_inspect="$tmp/host-$host_port.json"
  runtime_json host "$host_port" "$host_url" > "$host_inspect"
  FAKE_DOCKER_INSPECT="$host_inspect" assert_sub2api_runtime_contract sub2api "$image_id" host "$host_port"

  bridge_inspect="$tmp/bridge-$host_port.json"
  runtime_json bridge "$host_port" "$bridge_url" > "$bridge_inspect"
  FAKE_DOCKER_INSPECT="$bridge_inspect" assert_sub2api_runtime_contract sub2api "$image_id" bridge "$host_port"

  wrong_bridge="$tmp/bridge-wrong-$host_port.json"
  runtime_json bridge "$host_port" "$bridge_url" | jq '.[0].NetworkSettings.Ports["8080/tcp"][0].HostPort = "19999"' > "$wrong_bridge"
  if FAKE_DOCKER_INSPECT="$wrong_bridge" assert_sub2api_runtime_contract sub2api "$image_id" bridge "$host_port"; then
    exit 1
  fi

  for mode in host bridge; do
    override="$tmp/override-$mode-$host_port.yml"
    write_release_active_override "$override" "$image_id" "$instance_id" "$host_port" "$mode"
    grep -Fxq '    container_name: sub2api' "$override"
    grep -Fq "SUB2API_INSTANCE_ID: $instance_id" "$override"
    if [[ $mode == host ]]; then
      grep -Fq "SERVER_PORT: \"$host_port\"" "$override"
      grep -Fq "$host_url" "$override"
    else
      ! grep -Fq 'SERVER_HOST:' "$override"
      ! grep -Fq 'SERVER_PORT:' "$override"
      grep -Fq "$bridge_url" "$override"
    fi
  done
done

printf 'compose_contract_integration=pass\n'
