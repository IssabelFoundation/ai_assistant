%global debug_package %{nil}

Name:           issabel-mcp
Version:        0.1.0
Release:        12%{?dist}
Summary:        Restricted MCP and BYOK service for Issabel
License:        GPLv3+
URL:            https://www.issabel.org
Source0:        %{name}-%{version}.tar.gz
BuildRequires:  golang >= 1.20
Requires:       openssl, systemd

%description
Local MCP stdio server and same-origin AI provider gateway. Mutation tools only
create plans and cannot approve or execute PBX changes.

%prep
%setup -q

%build
cd mcp
# RPM sources are built from a tarball, without repository metadata.
CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags "-s -w -B gobuildid" -o ../issabel-mcp ./cmd/issabel-mcp

%install
install -Dpm0755 issabel-mcp %{buildroot}%{_sbindir}/issabel-mcp
install -Dpm0755 packaging/issabel-mcp-ssh %{buildroot}%{_sbindir}/issabel-mcp-ssh
install -Dpm0440 packaging/issabel-mcp-remote.sudoers %{buildroot}%{_datadir}/doc/%{name}/issabel-mcp-remote.sudoers
install -Dpm0644 packaging/issabel-mcp.service %{buildroot}%{_unitdir}/issabel-mcp.service
install -Dpm0644 packaging/issabel-mcp.sysconfig %{buildroot}%{_sysconfdir}/sysconfig/issabel-mcp
install -d -m0750 %{buildroot}%{_sysconfdir}/issabel-mcp
install -d -m0700 %{buildroot}%{_localstatedir}/lib/issabel-mcp

%pre
getent group issabel-ai >/dev/null || groupadd -r issabel-ai
getent passwd issabel-mcp >/dev/null || useradd -r -g issabel-ai -d /var/lib/issabel-mcp -s /sbin/nologin issabel-mcp
exit 0

%post
install -d -m0750 -o issabel-mcp -g issabel-ai /etc/issabel-mcp
install -d -m0700 -o issabel-mcp -g issabel-ai /var/lib/issabel-mcp
if [ ! -s /etc/issabel-mcp/private.pem ]; then
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out /etc/issabel-mcp/private.pem
  openssl pkey -in /etc/issabel-mcp/private.pem -pubout -out /etc/issabel-mcp/public.pem
fi
[ -s /etc/issabel-mcp/master.key ] || openssl rand -base64 32 > /etc/issabel-mcp/master.key
[ -s /etc/issabel-mcp/web.secret ] || openssl rand -base64 32 > /etc/issabel-mcp/web.secret
chown issabel-mcp:issabel-ai /etc/issabel-mcp/private.pem /etc/issabel-mcp/master.key /etc/issabel-mcp/web.secret
chmod 0600 /etc/issabel-mcp/private.pem /etc/issabel-mcp/master.key
chmod 0640 /etc/issabel-mcp/web.secret
chmod 0644 /etc/issabel-mcp/public.pem
%systemd_post issabel-mcp.service

%preun
%systemd_preun issabel-mcp.service

%postun
%systemd_postun_with_restart issabel-mcp.service

%files
%{_sbindir}/issabel-mcp
%{_sbindir}/issabel-mcp-ssh
%{_datadir}/doc/%{name}/issabel-mcp-remote.sudoers
%{_unitdir}/issabel-mcp.service
%config(noreplace) %{_sysconfdir}/sysconfig/issabel-mcp
%dir %attr(0750,issabel-mcp,issabel-ai) %{_sysconfdir}/issabel-mcp
%dir %attr(0700,issabel-mcp,issabel-ai) %{_localstatedir}/lib/issabel-mcp

%changelog
* Wed Sep 30 2026 Issabel Foundation <security@issabel.org> - 0.1.0-12
- Persist plan outcome events and associate them with their conversations

* Tue Sep 29 2026 Issabel Foundation <security@issabel.org> - 0.1.0-11
- Add a GNU ELF build ID to the static Go binary for RPM compatibility
- Disable empty debug packages for the stripped, trimpath Go binary

* Tue Sep 29 2026 Issabel Foundation <security@issabel.org> - 0.1.0-10
- Create packaged configuration and state directories during RPM installation

* Tue Sep 29 2026 Issabel Foundation <security@issabel.org> - 0.1.0-9
- Add restricted queue listing and approval-bound queue deletion plans

* Tue Sep 29 2026 Issabel Foundation <security@issabel.org> - 0.1.0-8
- Treat queue failover type as authoritative and discard destinations for hangup

* Mon Sep 28 2026 Issabel Foundation <security@issabel.org> - 0.1.0-7
- Add approval-bound queue creation plans with static and dynamic agents

* Mon Sep 28 2026 Issabel Foundation <security@issabel.org> - 0.1.0-6
- Require a discriminated list or range selector for extension plan tools
- Normalize legacy empty lists without producing ambiguous PBX requests

* Mon Sep 28 2026 Issabel Foundation <security@issabel.org> - 0.1.0-5
- Log secret-free LLM tool selector provenance for troubleshooting

* Mon Sep 28 2026 Issabel Foundation <security@issabel.org> - 0.1.0-4
- Canonicalize equivalent extension selectors and stop repeated invalid tool calls
- Correlate PBX validation failures with the local request audit

* Mon Sep 28 2026 Issabel Foundation <security@issabel.org> - 0.1.0-3
- Support local PBX API certificates without weakening remote TLS

* Mon Sep 28 2026 Issabel Foundation <security@issabel.org> - 0.1.0-2
- Use the OpenAI Responses API for function tools

* Mon Sep 28 2026 Issabel Foundation <security@issabel.org> - 0.1.0-1
- Initial restricted MCP and BYOK service
