Name:           issabel-ai-assistant
Version:        0.1.0
Release:        1%{?dist}
Summary:        Issabel web interface for the AI assistant
License:        GPLv3+
URL:            https://www.issabel.org
Source0:        %{name}-%{version}.tar.gz
BuildArch:      noarch
Requires:       issabel-mcp = %{version}
Requires:       php, php-curl, php-pdo, shadow-utils
Requires(post): issabel-mcp = %{version}, issabel-framework, shadow-utils, php-cli, systemd

%description
Same-origin Issabel chat, per-user BYOK configuration, conversation history,
and human review/approval interface for extension change plans.

%prep
%setup -q

%build

%install
install -Dpm0644 web/ai-assistant/index.php %{buildroot}/var/www/html/modules/issabel_ai_assistant/index.php
install -Dpm0644 web/ai-assistant/module.xml %{buildroot}/var/www/html/modules/issabel_ai_assistant/module.xml
install -Dpm0644 web/ai-assistant/menu.xml %{buildroot}/usr/share/issabel/module_installer/%{name}-%{version}-%{release}/menu.xml
install -Dpm0644 web/ai-assistant/themes/default/css/assistant.css %{buildroot}/var/www/html/modules/issabel_ai_assistant/themes/default/css/assistant.css
install -Dpm0644 web/ai-assistant/themes/default/js/assistant.js %{buildroot}/var/www/html/modules/issabel_ai_assistant/themes/default/js/assistant.js
install -Dpm0644 web/ai-assistant/lang/es.lang %{buildroot}/var/www/html/modules/issabel_ai_assistant/lang/es.lang
install -Dpm0644 web/ai-assistant/lang/en.lang %{buildroot}/var/www/html/modules/issabel_ai_assistant/lang/en.lang
install -Dpm0755 web/ai-assistant/setup/install.php %{buildroot}/var/www/html/modules/issabel_ai_assistant/setup/install.php

%post
# Issabel runs httpd as asterisk; also support standard Apache installations.
for web_user in asterisk apache; do
    if getent passwd "$web_user" >/dev/null 2>&1; then
        usermod -a -G issabel-ai "$web_user" || exit 1
    fi
done
if command -v issabel-menumerge >/dev/null 2>&1; then issabel-menumerge /usr/share/issabel/module_installer/%{name}-%{version}-%{release}/menu.xml; fi
php /var/www/html/modules/issabel_ai_assistant/setup/install.php
# Refresh supplementary groups in PHP workers (Issabel 5) and mod_php (Issabel 4).
if systemctl is-active --quiet php-fpm; then systemctl restart php-fpm || exit 1; fi
if systemctl is-active --quiet httpd; then systemctl restart httpd; fi

%preun
if [ $1 -eq 0 ] && command -v issabel-menuremove >/dev/null 2>&1; then issabel-menuremove issabel_ai_assistant; fi

%files
/var/www/html/modules/issabel_ai_assistant
/usr/share/issabel/module_installer/%{name}-%{version}-%{release}/menu.xml

%changelog
* Wed Sep 30 2026 Issabel Foundation <security@issabel.org> - 0.1.0-16
- Replace browser dialogs with accessible styled modals and scrollable audit output

* Wed Sep 30 2026 Issabel Foundation <security@issabel.org> - 0.1.0-15
- Mark failed execution cards as terminal and disable approval and cancellation

* Wed Sep 30 2026 Issabel Foundation <security@issabel.org> - 0.1.0-14
- Show and record server-observed plan outcomes in chat history

* Wed Sep 30 2026 Issabel Foundation <security@issabel.org> - 0.1.0-13
- Render Markdown tables with column alignment and horizontal scrolling

* Wed Sep 30 2026 Issabel Foundation <security@issabel.org> - 0.1.0-12
- Render basic Markdown safely in assistant replies, streaming and history

* Wed Sep 30 2026 Issabel Foundation <security@issabel.org> - 0.1.0-11
- Restart active PHP-FPM workers to apply web secret group access on Issabel 5

* Wed Sep 30 2026 Issabel Foundation <security@issabel.org> - 0.1.0-10
- Log distinct secret read and format failures without exposing secret contents

* Wed Sep 30 2026 Issabel Foundation <security@issabel.org> - 0.1.0-9
- Grant the Issabel asterisk web account access to the local authentication secret

* Wed Sep 30 2026 Issabel Foundation <security@issabel.org> - 0.1.0-8
- Constrain provider controls to their columns and wrap settings for narrow containers

* Wed Sep 30 2026 Issabel Foundation <security@issabel.org> - 0.1.0-7
- Move the assistant menu under PBX and repair administrator module access on upgrade

* Tue Sep 29 2026 Issabel Foundation <security@issabel.org> - 0.1.0-6
- Add queue read ACL and display queue deletion plans

* Mon Sep 28 2026 Issabel Foundation <security@issabel.org> - 0.1.0-5
- Display queue plans and register the queue planning permission

* Mon Sep 28 2026 Issabel Foundation <security@issabel.org> - 0.1.0-4
- Renew expired PBX API access tokens transparently for plan operations

* Mon Sep 28 2026 Issabel Foundation <security@issabel.org> - 0.1.0-3
- Keep module assets in the native themes/default hierarchy

* Mon Sep 28 2026 Issabel Foundation <security@issabel.org> - 0.1.0-2
- Render as a native Issabel module and scope all interface styles

* Mon Sep 28 2026 Issabel Foundation <security@issabel.org> - 0.1.0-1
- Initial assistant interface
