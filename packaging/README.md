# Deployment

## GitHub Actions

El workflow `.github/workflows/main.yml` construye ambos RPM en Rocky Linux 8
(EL8, x86_64), tomando nombre, versión y release de los specs de esta carpeta.
Se ejecuta con pushes a `main`, pull requests hacia `main` y manualmente desde
**Actions → RPM Build → Run workflow**.

Primero ejecuta los tests de Go y luego utiliza los helpers de empaquetado.
El spec de MCP compila el servidor con `CGO_ENABLED=0` y lo incluye en el RPM
como `/usr/sbin/issabel-mcp`; no hace falta instalar Go en el servidor destino.
El módulo web se empaqueta como `noarch`.

Cada ejecución exitosa publica dos artifacts, disponibles durante 30 días:

- `issabel-mcp-el8-x86_64`: RPM del servicio MCP compilado.
- `issabel-ai-assistant-el8-noarch`: RPM del módulo web para Issabel.

Se descargan desde **Actions → ejecución → Artifacts**.

En pushes a `main` y ejecuciones manuales sobre `main`, el workflow también
sube los dos RPM binarios por SSH al directorio remoto `rpm`, usando
`asternic/scp-action@master`, igual que `framework`. Los archivos quedan
directamente en `rpm/`, sin los directorios locales de construcción.
Usa los organization secrets `REPOHOST`, `REPOUSER`, `REPOPORT` y `BUILDKEY`,
que deben estar habilitados para este repositorio. Los pull requests y las
ejecuciones manuales de otras ramas solo construyen y generan artifacts.

Para activarlo, deben estar
commiteados y subidos tanto `.github/workflows/main.yml` como `packaging/`.
La construcción es independiente de `framework`; para usar los paquetes se
necesita una instalación de Issabel con la PBX API compatible.

## Construcción del RPM de `issabel-ai-assistant`

El spec necesita `issabel-ai-assistant-0.1.0.tar.gz` en el directorio
`SOURCES` de rpmbuild. Desde el repositorio completo, este helper genera
el tarball con los archivos de `web/ai-assistant` y construye el RPM
binario y el RPM de fuentes (`rpmbuild -ba`):

```sh
cd /ruta/al/repositorio/pbxapi
packaging/build-issabel-ai-assistant-rpm.sh
```

Para generar únicamente los fuentes y compilar por separado:

```sh
packaging/build-issabel-ai-assistant-rpm.sh --source-only
rpmbuild -ba /root/rpmbuild/SPECS/issabel-ai-assistant.spec
```

La ruta anterior corresponde al árbol habitual de root. El helper respeta
`rpm --eval '%{_topdir}'`; también permite seleccionar otro árbol:

```sh
RPMBUILD_TOPDIR=/ruta/rpmbuild packaging/build-issabel-ai-assistant-rpm.sh
```

El paquete requiere `issabel-mcp` de la misma versión para instalarse.

Desde la release `0.1.0-7`, el acceso aparece en **PBX → Asistente IA**.
La actualización mueve la entrada existente y asegura el acceso del grupo
administrador. Reconstruya el RPM con el helper e instálelo mediante
`yum update /ruta/issabel-ai-assistant-0.1.0-7*.noarch.rpm`.
Después cierre sesión y vuelva a entrar.

Para reparar el menú sin reconstruir el RPM, desde una copia actualizada
del repositorio en el servidor ejecute como root:

```sh
issabel-menumerge web/ai-assistant/menu.xml
php web/ai-assistant/setup/install.php
```

El acceso directo dentro de Issabel es `/index.php?menu=issabel_ai_assistant`.


## Construcción del RPM de `issabel-mcp`

El archivo `issabel-mcp.spec` declara `issabel-mcp-0.1.0.tar.gz` como
`Source0`. Por eso, ejecutar `rpmbuild -bb issabel-mcp.spec` directamente
requiere que ese tarball ya exista en `%{_topdir}/SOURCES`, normalmente
`/root/rpmbuild/SOURCES`.

El helper incluido crea la estructura de rpmbuild, genera el tarball con el
directorio raíz que espera `%setup` y construye el paquete:

```sh
cd /ruta/al/repositorio/pbxapi
packaging/build-issabel-mcp-rpm.sh
```

También puede generar únicamente `Source0` y ejecutar el build por separado:

```sh
packaging/build-issabel-mcp-rpm.sh --source-only
rpmbuild -bb /root/rpmbuild/SPECS/issabel-mcp.spec
```

Para usar otro árbol de construcción, defina `RPMBUILD_TOPDIR`:

```sh
RPMBUILD_TOPDIR=/ruta/rpmbuild packaging/build-issabel-mcp-rpm.sh
```

Build one static binary per target architecture:

```sh
cd mcp
mkdir -p ../build
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o ../build/issabel-mcp ./cmd/issabel-mcp
```

Use `GOARCH=arm64` for aarch64. Run `packaging/install.sh` from the repository root as root. The installer is idempotent, does not replace existing keys, and registers `menu.xml` when `issabel-menumerge` is available.

The idempotent installer registers independent Issabel ACL resources and grants them initially only to the administrator group, including `assistant.access`, `assistant.provider.configure`, `assistant.extensions.read`, `assistant.plans.create`, `assistant.queues.read`, `assistant.queues.plan`, `assistant.plans.approve`, and `assistant.audit.read`.

For optional external MCP access, create a locked, key-only Unix account and install the narrowly scoped sudo rule:

```sh
useradd --create-home --shell /bin/sh issabel-mcp-remote
passwd -l issabel-mcp-remote
install -m 0440 packaging/issabel-mcp-remote.sudoers /etc/sudoers.d/issabel-mcp-remote
visudo -cf /etc/sudoers.d/issabel-mcp-remote
```

Then restrict its `authorized_keys` entry:

```text
no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc,command="/usr/sbin/issabel-mcp-ssh" ssh-ed25519 AAAA...
```

Do not add the account to privileged groups or grant any broader sudo command. The stdio server runs as `issabel-mcp`, can read extensions and create/cancel plans, but has no approval or execution tool.

## PBX API TLS diagnostics

The daemon reads its PBX endpoint from `/etc/sysconfig/issabel-mcp`. Check the effective configuration and recent errors with:

```sh
grep '^ISSABEL_PBXAPI' /etc/sysconfig/issabel-mcp
systemctl show issabel-mcp -p Environment
journalctl -u issabel-mcp -n 100 --no-pager
curl -vk --max-time 10 https://127.0.0.1/pbxapi/
openssl s_client -connect 127.0.0.1:443 -servername "$(hostname -f)" -showcerts </dev/null
```

For a loopback URL, Issabel installations commonly present a certificate whose names do not include `127.0.0.1`. Use:

```ini
ISSABEL_PBXAPI_URL=https://127.0.0.1/pbxapi
ISSABEL_PBXAPI_TLS_INSECURE=true
```

The daemon refuses this TLS exception for non-loopback destinations. Restart it after changing the file:

```sh
systemctl restart issabel-mcp
journalctl -u issabel-mcp -n 50 --no-pager
```

The preferred strict setup is to use a hostname present in the certificate and, for a private CA, set `ISSABEL_PBXAPI_CA_FILE` to its PEM certificate while leaving `ISSABEL_PBXAPI_TLS_INSECURE=false`.

## Rejected plan troubleshooting

Requests rejected before a plan exists receive an `X-Issabel-Request-ID` header and a matching `request_id` in the JSON response. The PBX API stores a secret-free diagnostic event for 30 days in `/var/www/db/pbxapi-mcp.sqlite` and writes the same identifier to the Apache error log.

The daemon records each proposed extension-plan tool call before normalization. The entry includes only provider/model provenance and safe selector fields, so it can show whether the LLM supplied a list, a range, or both:

```sh
journalctl -u issabel-mcp --since "30 minutes ago" --no-pager | grep tool_call_proposed
```

```sh
sqlite3 -header -column /var/www/db/pbxapi-mcp.sqlite \
  "SELECT request_id,actor,method,path,status_code,datetime(created_at,'unixepoch') AS created_utc,detail FROM request_audit ORDER BY id DESC LIMIT 30;"

grep 'pbxapi.mcpplans' /var/log/httpd/error_log | tail -50
grep 'pbxapi.mcpplans' /var/log/httpd/ssl_error_log | tail -50
```

An administrator token with `extensions:audit` can also retrieve the most recent rejected requests through `GET /pbxapi/mcpplans?audit=rejected`. The `detail.request` object contains only the operation, profile, selector mode, explicit extensions, and range fields received by PBX API. Raw request bodies, prompts, API keys, device passwords, voicemail PINs and tokens are never written to this audit.

## “Assistant service is not configured”

Este error indica que PHP no puede leer o validar
`/etc/issabel-mcp/web.secret`. Issabel ejecuta httpd como `asterisk`;
las releases anteriores a `issabel-ai-assistant-0.1.0-9` solo agregaban
`apache` al grupo `issabel-ai`. Para reparar esa instalación como root:

```sh
usermod -a -G issabel-ai asterisk
if systemctl is-active --quiet php-fpm; then systemctl restart php-fpm; fi
systemctl restart httpd
runuser -u asterisk -- test -r /etc/issabel-mcp/web.secret && echo "Secreto accesible"
systemctl enable --now issabel-mcp
```

En Issabel 5 también es necesario reiniciar PHP-FPM para aplicar el nuevo
grupo a los procesos PHP. En Issabel 4 PHP se ejecuta como módulo de Apache.
Desde la release `0.1.0-11`, el RPM del asistente y el instalador manual
reinician PHP-FPM si está activo, además de httpd si está activo. Estos
reinicios interrumpen brevemente la interfaz web. Si sigue fallando, revisar propietario,
permisos y registros sin mostrar el contenido del secreto:

```sh
id asterisk
ls -ld /etc/issabel-mcp
ls -l /etc/issabel-mcp/web.secret
systemctl status issabel-mcp --no-pager
journalctl -u issabel-mcp -n 50 --no-pager
```

El directorio debe tener permisos `0750` y el secreto `0640`, ambos
con propietario `issabel-mcp:issabel-ai`. No regenerar las claves existentes
ni hacerlas legibles para todos los usuarios.

## Historial de ejecución de planes

Desde `issabel-ai-assistant-0.1.0-14` y `issabel-mcp-0.1.0-12`, el proxy PHP
registra el resultado de cada intento de aprobación/ejecución y el chat lo
muestra como mensaje de sistema. Actualizar ambos paquetes y reiniciar
`issabel-mcp` para habilitar esta integración.

Los eventos se guardan en la conversación que originó el plan; para planes
sin conversación local identificable, se crea una entrada “Resultado del plan”.
Se distinguen ejecución exitosa, error de aprobación, error de ejecución y
resultado incierto. Un fallo al guardar el historial se avisa por separado y
no cambia la respuesta de ejecución ni impide descargar el CSV. No se guardan
credenciales ni cuerpos de respuesta de ejecución en el historial.
Las ejecuciones anteriores a esta actualización no se reconstruyen automáticamente;
su registro sigue disponible en la auditoría del plan.
