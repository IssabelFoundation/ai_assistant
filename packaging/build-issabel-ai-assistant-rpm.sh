#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"
spec_file="${script_dir}/issabel-ai-assistant.spec"

if [[ ! -f "${spec_file}" ]]; then
    echo "Spec file not found: ${spec_file}" >&2
    exit 1
fi

package_name="$(awk '$1 == "Name:" { print $2; exit }' "${spec_file}")"
package_version="$(awk '$1 == "Version:" { print $2; exit }' "${spec_file}")"

if [[ -z "${package_name}" || -z "${package_version}" ]]; then
    echo "Unable to read Name or Version from ${spec_file}" >&2
    exit 1
fi

if [[ -n "${RPMBUILD_TOPDIR:-}" ]]; then
    rpmbuild_topdir="${RPMBUILD_TOPDIR}"
elif command -v rpm >/dev/null 2>&1; then
    rpmbuild_topdir="$(rpm --eval '%{_topdir}')"
else
    echo "rpm is unavailable. Set RPMBUILD_TOPDIR explicitly." >&2
    exit 1
fi

source_only=false
if [[ $# -eq 1 && "$1" == "--source-only" ]]; then
    source_only=true
elif [[ $# -gt 0 ]]; then
    echo "Usage: $0 [--source-only]" >&2
    exit 2
fi

for required_command in awk cp mktemp tar; do
    if ! command -v "${required_command}" >/dev/null 2>&1; then
        echo "Required command is unavailable: ${required_command}" >&2
        exit 1
    fi
done

mkdir -p \
    "${rpmbuild_topdir}/BUILD" \
    "${rpmbuild_topdir}/BUILDROOT" \
    "${rpmbuild_topdir}/RPMS" \
    "${rpmbuild_topdir}/SOURCES" \
    "${rpmbuild_topdir}/SPECS" \
    "${rpmbuild_topdir}/SRPMS"

archive_root="${package_name}-${package_version}"
source_archive="${rpmbuild_topdir}/SOURCES/${archive_root}.tar.gz"
staging_dir="$(mktemp -d)"

cleanup() {
    rm -rf -- "${staging_dir}"
}
trap cleanup EXIT

mkdir -p "${staging_dir}/${archive_root}"
mkdir -p "${staging_dir}/${archive_root}/web"
cp -a "${repo_root}/web/ai-assistant" "${staging_dir}/${archive_root}/web/"
cp -a "${repo_root}/packaging" "${staging_dir}/${archive_root}/"

tar -C "${staging_dir}" -czf "${source_archive}" "${archive_root}"
cp -a "${spec_file}" "${rpmbuild_topdir}/SPECS/issabel-ai-assistant.spec"

echo "Source archive created: ${source_archive}"

if [[ "${source_only}" == true ]]; then
    exit 0
fi

if ! command -v rpmbuild >/dev/null 2>&1; then
    echo "rpmbuild is unavailable. Install rpm-build or use --source-only." >&2
    exit 1
fi

rpmbuild \
    --define "_topdir ${rpmbuild_topdir}" \
    -ba "${rpmbuild_topdir}/SPECS/issabel-ai-assistant.spec"

echo "RPM build completed. Packages are under ${rpmbuild_topdir}/RPMS/ and ${rpmbuild_topdir}/SRPMS/."
