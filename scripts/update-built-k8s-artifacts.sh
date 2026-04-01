#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
KUBE_ROOT=$(cd "${SCRIPT_DIR}/.." && pwd -P)

DEFAULT_TAG="v1.32.0-service-cpuset"
DEFAULT_MASTER_NODE="cpu-16"
DEFAULT_WORKER_NODES="cpu-15,cpu-17"
DEFAULT_SOURCE_REGISTRY="registry.k8s.io"
DEFAULT_TARGET_REGISTRY="registry.local:5000"
DEFAULT_ARCH="amd64"
DEFAULT_REMOTE_TMP_ROOT="/tmp/k8s-built-artifacts-update"

KUBE_TAG="${KUBE_TAG:-${DEFAULT_TAG}}"
MASTER_NODE="${MASTER_NODE:-${DEFAULT_MASTER_NODE}}"
WORKER_NODES_CSV="${WORKER_NODES:-${DEFAULT_WORKER_NODES}}"
SOURCE_REGISTRY="${SOURCE_REGISTRY:-${DEFAULT_SOURCE_REGISTRY}}"
TARGET_REGISTRY="${TARGET_REGISTRY:-${DEFAULT_TARGET_REGISTRY}}"
ARCH="${ARCH:-${DEFAULT_ARCH}}"
REMOTE_TMP_ROOT="${REMOTE_TMP_ROOT:-${DEFAULT_REMOTE_TMP_ROOT}}"
SSH_USER="${SSH_USER:-}"
IMAGE_ARCHIVE_DIR="${IMAGE_ARCHIVE_DIR:-}"
BINARY_OUTPUT_DIR="${BINARY_OUTPUT_DIR:-}"
IMAGE_NAMES_CSV="${IMAGE_NAMES:-}"
DRY_RUN=false
SKIP_IMAGES=false
SKIP_BINARIES=false
SKIP_ROLLOUT=false

readonly DEFAULT_BINARIES=(kubectl kubelet kubeadm)
readonly DEFAULT_IMAGES=(kube-apiserver kube-controller-manager kube-scheduler kube-proxy kubectl)

usage() {
  cat <<EOF
Usage: $(basename "$0") [options]

Update locally built Kubernetes binaries and release images for the cpuset cluster.

Defaults:
  KUBE_TAG=${DEFAULT_TAG}
  MASTER_NODE=${DEFAULT_MASTER_NODE}
  WORKER_NODES=${DEFAULT_WORKER_NODES}
  SOURCE_REGISTRY=${DEFAULT_SOURCE_REGISTRY}
  TARGET_REGISTRY=${DEFAULT_TARGET_REGISTRY}
  ARCH=${DEFAULT_ARCH}

Options:
  --tag TAG                  Override the Kubernetes image tag.
  --master NODE              Override the master node name.
  --workers CSV              Override worker nodes, comma separated.
  --source-registry REG      Override the source registry.
  --target-registry REG      Override the target registry.
  --arch ARCH                Override the image architecture suffix.
  --binary-output-dir DIR    Use a specific directory for built binaries.
  --image-archive-dir DIR    Use a specific directory for built image tarballs.
  --image-names CSV          Override image names, comma separated.
  --ssh-user USER            Use USER@host for ssh/scp.
  --skip-binaries            Only retag and push images.
  --skip-images              Only update kubectl/kubelet/kubeadm.
  --skip-rollout             Do not update kube-system workloads to the new images.
  --dry-run                  Print actions without executing them.
  -h, --help                 Show this help.

Notes:
  - Existing kubectl/kubelet/kubeadm binaries are overwritten in place without backup.
  - kubelet is restarted on each node after the binaries are installed.
  - After pushing images, kube-apiserver/kube-controller-manager/kube-scheduler static pods
    on the master and the kube-proxy DaemonSet are updated to the new image refs.
  - The script expects passwordless ssh/scp plus sudo access on cpu-16/cpu-15/cpu-17.
EOF
}

log() {
  printf '[INFO] %s\n' "$*"
}

warn() {
  printf '[WARN] %s\n' "$*" >&2
}

die() {
  printf '[ERROR] %s\n' "$*" >&2
  exit 1
}

run() {
  if "${DRY_RUN}"; then
    printf '[DRYRUN] '
    printf '%q ' "$@"
    printf '\n'
    return 0
  fi

  "$@"
}

trim() {
  local value="${1:-}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "${value}"
}

csv_to_array() {
  local csv="${1:-}"
  local -n out_ref=$2
  local IFS=','
  local raw=()
  out_ref=()

  read -r -a raw <<< "${csv}"
  for item in "${raw[@]}"; do
    item=$(trim "${item}")
    if [[ -n "${item}" ]]; then
      out_ref+=("${item}")
    fi
  done
}

ssh_target() {
  local node=$1
  if [[ -n "${SSH_USER}" ]]; then
    printf '%s@%s' "${SSH_USER}" "${node}"
  else
    printf '%s' "${node}"
  fi
}

local_hostname_short() {
  hostname -s 2>/dev/null || hostname
}

is_local_node() {
  local node=$1
  local short
  local full

  short=$(local_hostname_short)
  full=$(hostname 2>/dev/null || true)

  [[ "${node}" == "${short}" || -n "${full}" && "${node}" == "${full}" ]]
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --tag)
        KUBE_TAG=$2
        shift 2
        ;;
      --master)
        MASTER_NODE=$2
        shift 2
        ;;
      --workers)
        WORKER_NODES_CSV=$2
        shift 2
        ;;
      --source-registry)
        SOURCE_REGISTRY=$2
        shift 2
        ;;
      --target-registry)
        TARGET_REGISTRY=$2
        shift 2
        ;;
      --arch)
        ARCH=$2
        shift 2
        ;;
      --binary-output-dir)
        BINARY_OUTPUT_DIR=$2
        shift 2
        ;;
      --image-archive-dir)
        IMAGE_ARCHIVE_DIR=$2
        shift 2
        ;;
      --image-names)
        IMAGE_NAMES_CSV=$2
        shift 2
        ;;
      --ssh-user)
        SSH_USER=$2
        shift 2
        ;;
      --skip-binaries)
        SKIP_BINARIES=true
        shift
        ;;
      --skip-images)
        SKIP_IMAGES=true
        shift
        ;;
      --skip-rollout)
        SKIP_ROLLOUT=true
        shift
        ;;
      --dry-run)
        DRY_RUN=true
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        die "unknown option: $1"
        ;;
    esac
  done
}

discover_binary_output_dir() {
  local candidates=()

  if [[ -n "${BINARY_OUTPUT_DIR}" ]]; then
    candidates+=("${BINARY_OUTPUT_DIR}")
  fi

  candidates+=(
    "${KUBE_ROOT}/_output/local/bin/linux/${ARCH}"
    "${KUBE_ROOT}/_output/dockerized/bin/linux/${ARCH}"
    "${KUBE_ROOT}/_output/bin"
  )

  local candidate
  for candidate in "${candidates[@]}"; do
    if [[ -d "${candidate}" ]]; then
      printf '%s\n' "${candidate}"
      return 0
    fi
  done

  if [[ -d "${KUBE_ROOT}/_output" ]]; then
    candidate=$(find "${KUBE_ROOT}/_output" -type f -path "*/linux/${ARCH}/kubelet" -print -quit 2>/dev/null || true)
    if [[ -n "${candidate}" ]]; then
      dirname "${candidate}"
      return 0
    fi
  fi

  die "could not locate built binaries under ${KUBE_ROOT}/_output; build kubectl/kubelet/kubeadm first or pass --binary-output-dir"
}

discover_image_archive_dir() {
  local candidates=()

  if [[ -n "${IMAGE_ARCHIVE_DIR}" ]]; then
    candidates+=("${IMAGE_ARCHIVE_DIR}")
  fi

  candidates+=("${KUBE_ROOT}/_output/release-images/${ARCH}")

  local candidate
  for candidate in "${candidates[@]}"; do
    if [[ -d "${candidate}" ]]; then
      printf '%s\n' "${candidate}"
      return 0
    fi
  done

  candidate=$(find "${KUBE_ROOT}/_output" -type d -path "*/release-images/${ARCH}" -print -quit 2>/dev/null || true)
  if [[ -n "${candidate}" ]]; then
    printf '%s\n' "${candidate}"
    return 0
  fi

  die "could not locate built image tarballs under ${KUBE_ROOT}/_output; run build/release-images.sh first or pass --image-archive-dir"
}

resolve_local_binary_path() {
  local binary=$1
  local output_dir=$2
  local candidate

  candidate="${output_dir}/${binary}"
  if [[ -f "${candidate}" ]]; then
    printf '%s\n' "${candidate}"
    return 0
  fi

  candidate=$(find "${KUBE_ROOT}/_output" -type f \( -path "*/linux/${ARCH}/${binary}" -o -path "*/bin/${binary}" \) -print -quit 2>/dev/null || true)
  if [[ -n "${candidate}" ]]; then
    printf '%s\n' "${candidate}"
    return 0
  fi

  die "could not locate built binary ${binary}"
}

detect_install_path_local() {
  local binary=$1
  local path

  path=$(command -v "${binary}" 2>/dev/null || true)
  if [[ -n "${path}" ]]; then
    printf '%s\n' "${path}"
    return 0
  fi

  for path in "/usr/bin/${binary}" "/usr/local/bin/${binary}"; do
    if [[ -e "${path}" ]]; then
      printf '%s\n' "${path}"
      return 0
    fi
  done

  printf '/usr/local/bin/%s\n' "${binary}"
}

build_remote_install_script() {
  local tmp_dir=$1
  shift
  local binaries=("$@")
  local quoted_tmp_dir
  quoted_tmp_dir=$(printf '%q' "${tmp_dir}")
  local binary_literal
  binary_literal=$(printf '%q ' "${binaries[@]}")

  cat <<EOF
set -euo pipefail
tmp_dir=${quoted_tmp_dir}
binaries=(${binary_literal})

detect_target_path() {
  local binary=\$1
  local path

  path=\$(command -v "\${binary}" 2>/dev/null || true)
  if [[ -n "\${path}" ]]; then
    printf '%s\n' "\${path}"
    return 0
  fi

  for path in "/usr/bin/\${binary}" "/usr/local/bin/\${binary}"; do
    if [[ -e "\${path}" ]]; then
      printf '%s\n' "\${path}"
      return 0
    fi
  done

  printf '/usr/local/bin/%s\n' "\${binary}"
}

for binary in "\${binaries[@]}"; do
  target_path=\$(detect_target_path "\${binary}")
  sudo install -m 0755 "\${tmp_dir}/\${binary}" "\${target_path}"
done

if command -v systemctl >/dev/null 2>&1; then
  sudo systemctl restart kubelet
  sudo systemctl is-active --quiet kubelet
fi

rm -f "\${tmp_dir}/"*
rmdir "\${tmp_dir}" 2>/dev/null || true
EOF
}

update_binaries() {
  local output_dir=$1
  local -a binaries=("${DEFAULT_BINARIES[@]}")
  local -a worker_nodes=()
  local -a all_nodes=()
  local -a local_sources=()
  local node
  local binary
  local tmp_dir="${REMOTE_TMP_ROOT}/$(date +%Y%m%d%H%M%S)"

  csv_to_array "${WORKER_NODES_CSV}" worker_nodes
  all_nodes=("${MASTER_NODE}" "${worker_nodes[@]}")

  for binary in "${binaries[@]}"; do
    local_sources+=("$(resolve_local_binary_path "${binary}" "${output_dir}")")
  done

  log "Updating kubectl/kubelet/kubeadm from ${output_dir}"

  for node in "${all_nodes[@]}"; do
    if is_local_node "${node}"; then
      log "Installing binaries on local node ${node}"
      local idx
      for idx in "${!binaries[@]}"; do
        local target_path
        target_path=$(detect_install_path_local "${binaries[$idx]}")
        run sudo install -m 0755 "${local_sources[$idx]}" "${target_path}"
      done
      run sudo systemctl restart kubelet
      run sudo systemctl is-active --quiet kubelet
      continue
    fi

    local remote
    remote=$(ssh_target "${node}")
    log "Copying binaries to ${node}"
    run ssh "${remote}" "mkdir -p $(printf '%q' "${tmp_dir}")"
    run scp "${local_sources[@]}" "${remote}:$(printf '%q' "${tmp_dir}")/"
    run ssh "${remote}" "$(build_remote_install_script "${tmp_dir}" "${binaries[@]}")"
  done
}

image_ref_for() {
  local image_name=$1
  printf '%s/%s-%s:%s\n' "${TARGET_REGISTRY}" "${image_name}" "${ARCH}" "${KUBE_TAG}"
}

discover_image_names() {
  local image_dir=$1
  local -n out_ref=$2
  out_ref=()

  if [[ -n "${IMAGE_NAMES_CSV}" ]]; then
    csv_to_array "${IMAGE_NAMES_CSV}" out_ref
    return 0
  fi

  local image_name
  for image_name in "${DEFAULT_IMAGES[@]}"; do
    if [[ -f "${image_dir}/${image_name}.tar" ]]; then
      out_ref+=("${image_name}")
    fi
  done

  if [[ ${#out_ref[@]} -eq 0 ]]; then
    local archive
    while IFS= read -r archive; do
      out_ref+=("$(basename "${archive}" .tar)")
    done < <(find "${image_dir}" -maxdepth 1 -type f -name '*.tar' | sort)
  fi
}

ensure_source_image_present() {
  local image_name=$1
  local archive_dir=$2
  local source_ref=$3
  local target_ref=$4
  local archive_path="${archive_dir}/${image_name}.tar"

  if docker image inspect "${source_ref}" >/dev/null 2>&1 || docker image inspect "${target_ref}" >/dev/null 2>&1; then
    return 0
  fi

  [[ -f "${archive_path}" ]] || die "missing image archive ${archive_path}"

  log "Loading ${archive_path}"
  run docker load -i "${archive_path}"
}

push_images() {
  local archive_dir=$1
  local -a image_names=()
  local image_name

  discover_image_names "${archive_dir}" image_names
  if [[ ${#image_names[@]} -eq 0 ]]; then
    die "no images found in ${archive_dir}"
  fi

  log "Retagging and pushing images from ${archive_dir}"

  for image_name in "${image_names[@]}"; do
    local source_ref="${SOURCE_REGISTRY}/${image_name}-${ARCH}:${KUBE_TAG}"
    local target_ref
    target_ref=$(image_ref_for "${image_name}")

    ensure_source_image_present "${image_name}" "${archive_dir}" "${source_ref}" "${target_ref}"

    if docker image inspect "${source_ref}" >/dev/null 2>&1; then
      run docker tag "${source_ref}" "${target_ref}"
    elif ! docker image inspect "${target_ref}" >/dev/null 2>&1; then
      die "could not find ${source_ref} or ${target_ref} after loading ${image_name}.tar"
    fi

    run docker push "${target_ref}"
  done
}

build_master_rollout_script() {
  local master_node=$1
  local apiserver_image=$2
  local controller_manager_image=$3
  local scheduler_image=$4
  local kube_proxy_image=$5
  local timeout_seconds=300

  cat <<EOF
set -euo pipefail

kubeconfig=/etc/kubernetes/admin.conf
timeout_seconds=${timeout_seconds}
master_node=$(printf '%q' "${master_node}")
apiserver_image=$(printf '%q' "${apiserver_image}")
controller_manager_image=$(printf '%q' "${controller_manager_image}")
scheduler_image=$(printf '%q' "${scheduler_image}")
kube_proxy_image=$(printf '%q' "${kube_proxy_image}")

update_static_pod_manifest() {
  local component=\$1
  local image_ref=\$2
  local manifest="/etc/kubernetes/manifests/\${component}.yaml"

  [[ -f "\${manifest}" ]] || {
    echo "missing manifest: \${manifest}" >&2
    return 1
  }

  sudo sed -i -E "0,/^[[:space:]]*image:[[:space:]]*/s#(^[[:space:]]*image:[[:space:]]*).*$#\\\\1\${image_ref}#" "\${manifest}"
}

wait_for_static_pod() {
  local pod_name=\$1
  local image_ref=\$2
  local deadline=\$((SECONDS + timeout_seconds))
  local current_image
  local ready

  while (( SECONDS < deadline )); do
    current_image=\$(sudo kubectl --kubeconfig="\${kubeconfig}" -n kube-system get pod "\${pod_name}" -o jsonpath='{.spec.containers[0].image}' 2>/dev/null || true)
    ready=\$(sudo kubectl --kubeconfig="\${kubeconfig}" -n kube-system get pod "\${pod_name}" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null || true)

    if [[ "\${current_image}" == "\${image_ref}" && "\${ready}" == "true" ]]; then
      return 0
    fi

    sleep 5
  done

  echo "timed out waiting for \${pod_name} to use \${image_ref}" >&2
  return 1
}

update_static_pod_manifest kube-apiserver "\${apiserver_image}"
wait_for_static_pod "kube-apiserver-\${master_node}" "\${apiserver_image}"

update_static_pod_manifest kube-controller-manager "\${controller_manager_image}"
wait_for_static_pod "kube-controller-manager-\${master_node}" "\${controller_manager_image}"

update_static_pod_manifest kube-scheduler "\${scheduler_image}"
wait_for_static_pod "kube-scheduler-\${master_node}" "\${scheduler_image}"

sudo kubectl --kubeconfig="\${kubeconfig}" -n kube-system set image daemonset/kube-proxy kube-proxy="\${kube_proxy_image}" >/dev/null
sudo kubectl --kubeconfig="\${kubeconfig}" -n kube-system rollout status daemonset/kube-proxy --timeout=5m
EOF
}

rollout_kube_system_images() {
  local apiserver_image
  local controller_manager_image
  local scheduler_image
  local kube_proxy_image

  apiserver_image=$(image_ref_for "kube-apiserver")
  controller_manager_image=$(image_ref_for "kube-controller-manager")
  scheduler_image=$(image_ref_for "kube-scheduler")
  kube_proxy_image=$(image_ref_for "kube-proxy")

  log "Updating kube-system workloads to the new control-plane and kube-proxy images"

  if is_local_node "${MASTER_NODE}"; then
    run bash -lc "$(build_master_rollout_script "${MASTER_NODE}" "${apiserver_image}" "${controller_manager_image}" "${scheduler_image}" "${kube_proxy_image}")"
  else
    local remote
    remote=$(ssh_target "${MASTER_NODE}")
    run ssh "${remote}" "$(build_master_rollout_script "${MASTER_NODE}" "${apiserver_image}" "${controller_manager_image}" "${scheduler_image}" "${kube_proxy_image}")"
  fi
}

main() {
  parse_args "$@"

  if "${SKIP_BINARIES}" && "${SKIP_IMAGES}"; then
    die "nothing to do: both --skip-binaries and --skip-images were set"
  fi

  if ! "${SKIP_BINARIES}"; then
    local binary_output_dir
    binary_output_dir=$(discover_binary_output_dir)
    update_binaries "${binary_output_dir}"
  fi

  if ! "${SKIP_IMAGES}"; then
    local image_archive_dir
    image_archive_dir=$(discover_image_archive_dir)
    push_images "${image_archive_dir}"

    if ! "${SKIP_ROLLOUT}"; then
      rollout_kube_system_images
    fi
  fi

  log "Update completed successfully"
}

main "$@"
