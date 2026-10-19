# Backend ligero opcional — API v1

PHP 8.3 + PDO SQLite, sin framework, Composer, contenedores, proceso permanente ni base de datos externa. Se ejecuta bajo demanda en el alojamiento existente. URL: **https://ups.omstudioit.com/api/v1**. El sitio, soporte y privacidad conservan sus rutas. El modo local de iOS no depende de este servicio.

Este componente recibe al agente y ofrece datos autenticados para integrar después el cliente móvil. **La app publicada y la 1.1 todavía no consultan esta API. No incluye APNs, cuentas públicas, recuperación de acceso, QR ni panel web.** La primera etapa es un piloto con espacios privados creados por el operador; no activar su distribución pública como modo remoto terminado.

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

Este diseño evita procesos permanentes y dependencias, pero no promete capacidad ilimitada del hosting compartido. Vigilar disco, latencia y errores antes de ampliar el piloto. iOS remoto, autenticación/recuperación para usuarios finales, límites comerciales y APNs son desarrollos posteriores. El polling del agente no elimina las restricciones de widgets o ejecución en segundo plano de iOS.

## Pruebas

```sh
php backend/tests/run.php
python3 backend/scripts/bundle.py
go build -o /tmp/nut-ups-agent ./cmd/nut-ups-agent
python3 backend/tests/http_integration.py --php /ruta/php --agent /tmp/nut-ups-agent
go test -race ./...
go vet ./...
```

20 escenarios de contrato/seguridad y 6 de integración con el binario real. Las pruebas utilizan un NUT simulado y no ejecutan controles del UPS. El despliegue se verifica adicionalmente por HTTPS con un espacio sintético, que se elimina después. Sigue pendiente la instalación física del agente en Raspberry Pi.
