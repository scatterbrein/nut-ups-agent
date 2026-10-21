# Backend ligero opcional — API v1

PHP 8.3 + PDO SQLite, sin framework, Composer, contenedores, proceso permanente ni base de datos externa. Se ejecuta bajo demanda en el alojamiento existente. URL: **https://ups.omstudioit.com/api/v1**. El sitio, soporte y privacidad conservan sus rutas. El modo local de iOS no depende de este servicio.

Este componente recibe al agente y ofrece datos autenticados para integrar después el cliente móvil. **La app publicada y la 1.1 no consultan esta API. La 1.2 en desarrollo incorpora la fuente remota y vinculación de iPhone por código. La integración APNs está desplegada y tiene Sandbox/Production configurados, pendiente de entrega física al iPhone; no incluye cuentas públicas, recuperación de acceso, QR ni panel web.** La primera etapa es un piloto con espacios privados creados por el operador; no activar su distribución pública como modo remoto terminado.

## Funcionalidad

- Espacio separado por propietario; varios agentes, servidores y UPS por espacio.
- Vinculación de un solo uso de 10 minutos; token individual revocable por agente. Tokens de propietario, agente y administrador tienen permisos distintos. Se guardan hashes SHA-256, nunca el token original, en SQLite.
- Recepción durable y transaccional; reintentos deduplicados por agente, UUID y secuencia. Se confirma únicamente después del commit. Mensajes modificados que reutilizan una identidad se rechazan.
- Último estado con fecha original, detección de datos desactualizados y comunicación del agente separada de la disponibilidad de NUT. Tras 90 segundos sin comunicación se marca como no disponible al consultar, sin inventar un corte eléctrico.
- Historial con una muestra por minuto y muestras adicionales para eventos de UPS; eventos e historial consultables por 7 días. Las muestras del minuto en curso se actualizan; al refrescar una gráfica, volver a consultar la ventana reciente. La paginación usa orden de inserción, no orden cronológico del UPS; ordenar por `observed_at` al representar.
- Cola del agente independiente: perder internet no borra la última lectura. NUT y sus credenciales siguen en la red local. Lista estricta de variables permitidas y potencia calculada nuevamente en el receptor.
- Límites por IP/token, 100 mensajes por lote, 8 MiB por petición; respuestas privadas `no-store`, API `noindex`, sin CORS, cookies o sesiones.

## Operación del piloto

Guardar el archivo de credenciales del operador **fuera del repositorio**, con permisos 0600 y respaldo seguro. Contiene `public_url`, `owner_token`, `workspace_id` y, sólo para el operador inicial, `admin_token`. No distribuir el token de administrador ni subirlo al servidor web o a Git. La API no permite recuperar un token perdido.

Con Python 3 en el equipo del operador (no es dependencia del agente ni del servidor):

```sh
python3 backend/scripts/manage.py --credentials /ruta/privada/operator.json agents
python3 backend/scripts/manage.py --credentials /ruta/privada/operator.json pair --name 'Raspberry Pi Office'
```

El segundo comando muestra un código temporal. En la Raspberry Pi, detener el servicio si ya corre y ejecutar como su usuario:

```sh
sudo systemctl stop nut-ups-agent
sudo -u nut-ups-agent /usr/local/bin/nut-ups-agent \
  -pair-url https://ups.omstudioit.com/api/v1 -pair-code XXXX-XXXX-XXXX-XXXX
sudo systemctl start nut-ups-agent
```

El código es ilustrativo; usar el recién generado. `backend.url` debe estar vacío en la configuración para utilizar la vinculación. El agente guarda el token en un archivo privado separado y sólo su referencia en `backend-connection.json`. Si se perdió la respuesta de vinculación, solicitar otro código; volver a vincular el mismo agente rota su token. No borrar su estado para cambiar credenciales.

Consultar estado y eventos utilizando el `id` devuelto por `agents`:

```sh
python3 backend/scripts/manage.py --credentials /ruta/privada/operator.json state --agent UUID
python3 backend/scripts/manage.py --credentials /ruta/privada/operator.json events --agent UUID
python3 backend/scripts/manage.py --credentials /ruta/privada/operator.json history --agent UUID --device HASH
python3 backend/scripts/manage.py --credentials /ruta/privada/operator.json revoke --agent UUID
```

Para otro usuario del piloto, crear un espacio independiente. El archivo nuevo contiene sólo su token de propietario, sin privilegios administrativos:

```sh
python3 backend/scripts/manage.py --credentials /ruta/privada/operator.json \
  create-workspace --name 'Pilot user' --save /ruta/privada/pilot.json
```

## Endpoints

Todos bajo `/api/v1`; HTTPS obligatorio, cuerpos JSON. Las rutas privadas reciben `Authorization: Bearer TOKEN`. No introducir tokens en URLs.

| Método / ruta | Acceso | Resultado |
|---|---|---|
| GET `/health` | Público | Salud y versión; no datos de usuarios |
| POST `/bootstrap` | Secreto de instalación | Inicialización única; devuelve admin y primer propietario |
| POST `/admin/workspaces` | Administrador | Crea un propietario independiente |
| POST `/pairings` | Propietario | `{name}` → código temporal |
| POST `/pairings/claim` | Código temporal | `{code,agent_id}` → token del agente y endpoint |
| POST `/ingest` | Agente | Lote del [contrato](../docs/backend-contract.md) → IDs confirmados |
| GET `/agents` | Propietario | Inventario propio |
| GET `/agents/{id}/state` | Propietario | Último estado, antigüedad y disponibilidad |
| GET `/agents/{id}/history` | Propietario | `device_id` requerido; `after`, `cursor`, `limit` opcionales |
| GET `/agents/{id}/events` | Propietario | `after`, `cursor`, `limit` opcionales |
| POST `/agents/{id}/revoke` | Propietario | Revoca el token, mantiene el historial |
| DELETE `/workspace` | Propietario | Elimina espacio, agentes, datos y accesos |

`after` es Unix time en segundos; ventana predeterminada 24 horas, máximo 7 días. `limit` predeterminado 200, máximo 1000. `next_cursor` permite continuar la consulta. `state` devuelve `agent_available`, fuentes con `connection`, y dispositivos con `reading`, `stale`, `age_seconds`, `clock_skew`. Nunca confundir agente sin comunicación con UPS sin energía.

## Despliegue y mantenimiento

1. Comprobar PHP 8.3+, PDO/pdo_sqlite y directorio privado escribible fuera del document root. No se cambia la versión PHP del sitio desde este proyecto.
2. Ejecutar `python3 backend/scripts/bundle.py`; produce `backend/dist/ups-api.php` con el esquema incluido.
3. Integrar las reglas de `backend/deploy/routes.htaccess` **antes** de las rutas existentes. Bloquear acceso directo a PHP/config y `/api` antes de subir los archivos. No reemplazar las reglas del sitio por el fragmento.
4. Crear manualmente `ups-api-config.php` en el servidor, fuera de Git: devolver `storage_dir` (fuera de `public_html`), `public_url` y `bootstrap_hash` (SHA-256 de un secreto aleatorio de instalación de 32 bytes). No colocar el secreto original en ese archivo.
5. Subir únicamente `ups-api.php` y la configuración privada con el canal autenticado del alojamiento. La configuración PHP no debe ser accesible por HTTP. No subir fuentes, pruebas, base de datos, logs, tokens ni archivos de credenciales al document root.
6. POST HTTPS `/bootstrap` con ese secreto como Bearer y `{name}`; guardar la respuesta una sola vez en archivo privado. Confirmar que un segundo intento responde 409. Borrar el secreto de instalación local al terminar. Las actualizaciones de código **no** repiten bootstrap ni reemplazan la configuración.
7. Verificar `/health`, páginas existentes, 403 para los PHP directos y 401 para consultas sin token. El endpoint no debe ser almacenado por CDN/caché.

**No desplegar después un ZIP estático que vacíe `public_html`**: borraría el receptor. Actualizar selectivamente las páginas o preservar `ups-api.php`, `ups-api-config.php` y las reglas API. SQLite queda en `.ups-monitor-private` al lado de `public_html`; permisos 0700 en carpeta y 0600 en archivos nuevos.

La limpieza se ejecuta durante ingestiones, como máximo una vez por minuto y con borrado acotado de muestras/eventos/recibos. Las consultas filtran 7 días incluso si no hubo otra ingestión. El último estado permanece hasta eliminar su espacio. No hay cifrado de SQLite en reposo; usar los controles de acceso y copias del alojamiento. Para una copia consistente, usar SQLite Backup API o pausar escrituras antes de copiar la base. No copiar un SQLite activo como si fuese un archivo estático. Conservar el archivo privado de configuración y respaldos fuera del sitio. Para borrar por completo, revisar también los respaldos del proveedor.

Este diseño evita procesos permanentes y dependencias, pero no promete capacidad ilimitada del hosting compartido. Vigilar disco, latencia y errores antes de ampliar el piloto. Autenticación/recuperación para usuarios finales y límites comerciales son desarrollos posteriores. APNs está configurado en el hosting, pendiente de validación física. El polling del agente no elimina las restricciones de widgets o ejecución en segundo plano de iOS.

## Pruebas

```sh
php backend/tests/run.php
python3 backend/scripts/bundle.py
go build -o /tmp/nut-ups-agent ./cmd/nut-ups-agent
python3 backend/tests/http_integration.py --php /ruta/php --agent /tmp/nut-ups-agent
go test -race ./...
go vet ./...
```

24 escenarios de contrato/seguridad y 6 de integración con el binario real. Las pruebas utilizan un NUT simulado y no ejecutan controles del UPS. El despliegue se verifica adicionalmente por HTTPS con un espacio sintético, que se elimina después. Sigue pendiente la instalación física del agente en Raspberry Pi.

## Vinculación de iPhone — desarrollo 1.2

El operador genera un código de teléfono independiente del código del agente:

```sh
python3 backend/scripts/manage.py --credentials /ruta/privada/operator.json pair-phone
python3 backend/scripts/manage.py --credentials /ruta/privada/operator.json phones
python3 backend/scripts/manage.py --credentials /ruta/privada/operator.json revoke-phone --session UUID
```

En iOS: Connections → Connect Remote Agent → Phone link code → Link This iPhone → elegir agente → Save. Sin agente instalado, el mismo formulario puede generar un código para el agente; instalarlo y vincularlo antes de guardar la conexión de iPhone. No introducir tokens de administrador ni contraseñas NUT en iOS remoto.

Los códigos de teléfono duran 10 minutos y sólo se usan una vez. Cada iPhone recibe un token `ups_mobile_` individual, de lectura, guardado en Keychain; puede generar un código de agente o revocar su propia sesión, pero no borrar el espacio, revocar agentes o vincular otros teléfonos. Caduca a los 90 días; el operador puede revocarlo antes. Máximo 20 sesiones vigentes por espacio.

Endpoints adicionales: POST `/mobile-pairings` (propietario); POST `/mobile-pairings/claim` con `{code}` (público con prueba del código); GET `/mobile-sessions` y DELETE `/mobile-sessions/{id}` (propietario); DELETE `/mobile-session` (el propio iPhone). La API v1 continúa compatible con el agente 0.2.0. La migración SQLite 1→2 sólo añade tablas; crea una copia consistente `monitor-before-v2.sqlite` en la carpeta privada antes de actualizar una base existente.

El ZIP de backend de la prerelease 0.2.0 es anterior a las sesiones móviles y al esquema 2. No instalarlo sobre este despliegue: para actualizar usar el código actual de main y regenerar el bundle, conservando la configuración y la base privadas. Los paquetes del agente 0.2.0 sí siguen siendo compatibles.


## APNs opcional — desplegado; prueba física pendiente

La actualización de batería baja de 0.3.1 utiliza exclusivamente el porcentaje `battery.charge` y el umbral válido `battery.charge.low` del UPS; si falta el umbral, acepta el porcentaje configurado que adjunta el agente, o 10% para mensajes anteriores. Runtime y los estados `LB`/`FSD` por sí solos no generan eventos ni push. El backend valida la evidencia contra una lectura fresca del mismo mensaje, tanto al guardar el evento como al encolar la notificación. Los mensajes sin evidencia siguen confirmándose y sus lecturas se conservan. Los eventos históricos existentes no se borran. No cambia el esquema SQLite 3 ni la configuración APNs. Desplegar el bundle actual antes de instalar el agente 0.3.1, que añade campos opcionales de evidencia.

Migración SQLite 2→3 aditiva con copia privada consistente `monitor-before-v3.sqlite`. No usar después un bundle antiguo que sólo soporte schema 2. El agente 0.2.0 mantiene su contrato y no necesita reinstalación.

- GET `/push-subscriptions`: sesión móvil, lista sus agentes suscritos, nunca tokens.
- GET/POST/DELETE `/push-subscriptions/{agent_id}`: sólo sesión móvil propia y agente de su espacio. POST recibe `{device_token, environment, kinds}`; `environment` es `sandbox` o `production`, `kinds` es una lista separada por comas de `on_battery,power_restored,low_battery,voltage_drop`. POST devuelve 503 si las claves/HTTP2 no están preparados; nunca habilitar silenciosamente sin emisor.
- POST `/admin/push-drain`: sólo administrador, procesa como máximo 4 trabajos pendientes con límite temporal, devuelve contadores sin tokens.

Configuración privada en `storage_dir/apns/config.json` (0600), con `team_id`, `topic` y objetos `sandbox`/`production`, cada uno con `key_id` y `key_path` absoluto al `.p8` privado. El topic permitido es `local.UPSMonitor`. La clave ES256 se valida al firmar; JWT reutilizado hasta 45 minutos en caché privada. PHP necesita OpenSSL y cURL HTTP/2. Las claves nunca se incluyen en el bundle ni se reciben desde iOS o agentes.

Se encola por evento nuevo dentro de la transacción de ingestión y se envía después del commit. Leases serializan envíos concurrentes; UUID por suscripción/evento y `apns-id` estable, con `apns-collapse-id` para reintentos. Se reintentan errores transitorios y se eliminan tokens inválidos respetando la revisión de registro. La entrega de Apple es de mejor esfuerzo; una respuesta perdida puede ocasionar una repetición. Se descartan alertas expiradas (10 minutos), futuras o anteriores al consentimiento. El historial completo sigue conservándose. No hay alertas por silencio del agente ni procesos permanentes. El siguiente reporte del agente atiende reintentos; si todos dejan de reportar, no hay un cron automático de reintentos.

Pruebas adicionales:

```sh
php backend/tests/run.php
python3 backend/scripts/bundle.py
python3 backend/tests/push_http.py --php /ruta/php
```

Las pruebas generan claves efímeras propias, datos sintéticos y almacenamiento temporal. No utilizan los `.p8` reales ni envían notificaciones a Apple.


APNs: rutas administrativas nuevas GET/POST `/admin/apns` (estado/instalación) y POST `/admin/apns/probe` (token sintético sin destinatario). Sólo `ups_admin_`, nunca sesiones móviles ni propietarios. Utilizar `backend/scripts/apns.py` con un archivo de configuración privado. Las claves se instalan atómicamente fuera de la carpeta pública, con permisos 0600 y validación EC P-256; la otra clave se conserva. Nunca enviar material de claves en URL ni subirlo como archivo público. La herramienta fija el destino HTTPS del piloto y rechaza redirecciones. Estas rutas ya están desplegadas en el piloto. Sandbox y Production están listos y las pruebas desde el alojamiento llegaron a Apple sin errores de autenticación; la entrega a un iPhone real sigue pendiente.


## Instalación unificada — backend 0.4.0 (pendiente de despliegue)

Schema 4 añade `enrollments` con backup privado consistente `monitor-before-v4.sqlite`. No instalar un bundle schema 3 después de migrar. No cambia las claves APNs ni las conexiones existentes.

- POST `/installations`: registro temporal de instalación sin cuenta, con UUID aleatorio, prueba privada de 256 bits, código de ocho dígitos y token del agente. Guarda sólo hashes. No crea espacio, agente ni lecturas remotas todavía.
- POST `/installations/status`: requiere la prueba privada del Pi; devuelve sólo la identidad y si el usuario ya autorizó la vinculación.
- POST `/installations/claim`: exige consentimiento explícito, código y un token móvil generado en el iPhone. Crea un espacio y sus accesos aislados; los reintentos con el mismo token recuperan el mismo resultado. Otro token no puede reutilizar el código. No transmite secretos NUT.
- Límite de 20 solicitudes/minuto/IP, 300/minuto globales, 500 instalaciones pendientes y 100 altas completadas/día en el piloto. Caducidad de 30 minutos y bloqueo duradero tras cinco códigos incorrectos entre todas las IP.
- GET `/workspace-management`: una sesión móvil consulta si es la creadora de su espacio. Las sesiones creadoras/autorizadas desde el instalador pueden generar invitaciones POST `/mobile-pairings` y eliminar su espacio con DELETE `/workspace`. Las sesiones invitadas conservan sus restricciones anteriores. Borrar el espacio invalida el agente, sesiones y suscripciones por cascada.

El agente recibe su credencial antes de instalarla en su configuración privada únicamente después de que el estado confirme la autorización del iPhone. La app conserva su conexión NUT local y crea una conexión remota automática separada, sin copiar contraseñas a la nube. Este bundle debe desplegarse antes de publicar el instalador 0.4.0.

Recuperación: el operador del servidor puede ejecutar `sudo nut-ups-agent-setup` para generar otro código de acceso. El registro de un agente ya existente exige su credencial privada actual y conserva el espacio, identidad, histórico y token del agente. Las sesiones de administración se guardan en `workspace_managers` y no se revocan al preparar otro código; las invitaciones desde la app siguen siendo sesiones de consulta. Un código nuevo no desactiva los teléfonos anteriores.
