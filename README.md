# NUT UPS Monitor — agente local opcional (piloto 0.4.0)

Agente opcional para NUT UPS Monitor. Se ejecuta junto a NUT y conserva lecturas aunque el iPhone esté cerrado. La app 1.2 en desarrollo puede detectar una instalación preparada, crear su espacio privado y guardar las conexiones local y remota desde la misma pantalla. El acceso remoto y APNs utilizan **https://ups.omstudioit.com/api/v1**. La conexión exclusivamente local no necesita agente ni cuenta.

## Configuración sencilla (0.4.0)

1. Tener NUT funcionando en la Raspberry Pi / Linux con systemd.
2. Instalar el paquete del agente. La configuración inicial consulta `127.0.0.1:3493` y descubre todos los UPS; no cambia NUT.
3. Instalar NUT UPS Monitor en el iPhone y conectarlo a la misma red.
4. En Agregar servidor, introducir la IP del servidor NUT y probar la conexión. La app detecta el agente preparado. Elegir **Enable Remote Access**, escribir el código de ocho dígitos mostrado por el instalador y guardar. No se necesitan tokens ni un backend propio.

El código vence a los 30 minutos y admite cinco intentos fallidos. Si vence antes de terminar, ejecutar `sudo nut-ups-agent-setup` en el Pi para preparar otro código. Las instalaciones ya vinculadas conservan su conexión; este comando no las desvincula.

El instalador público de un comando está preparado en `scripts/install-online.sh` para la release `v0.4.0`. **No anunciar el comando público hasta publicar esa release y desplegar el backend compatible con instalaciones.** Después de publicarla:

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' https://raw.githubusercontent.com/scatterbrein/nut-ups-agent/main/scripts/install-online.sh | sh
```

El script detecta ARM64 / AMD64 / ARMv7, descarga el paquete versionado y verifica SHA-256 antes de invocar el instalador con sudo. Se conserva una copia del binario anterior; un fallo de actualización restaura el binario y servicio previos. Para una instalación manual desde el paquete verificado:

```sh
sudo sh scripts/install-and-start.sh ./nut-ups-agent-linux-arm64
```

Durante la vinculación se abre temporalmente TCP 3494 para un único saludo de descubrimiento en la red privada. No ofrece HTTP, controles, cambios de configuración ni secretos. No abrir ese puerto en el router. La app confirma la vinculación mediante HTTPS al servicio fijo de OM Studio IT; el código, contraseñas NUT y tokens nunca se intercambian por ese puerto local. Pasados 30 minutos, se cierra el descubrimiento. NUT puede permanecer activo sin acceso remoto.

Si el agente corre en un equipo distinto del servidor NUT, o éste requiere credenciales de lectura, conservar el flujo avanzado y ajustar `sources` de forma explícita. El flujo sencillo está pensado para instalar el agente junto a NUT.

En la configuración remota de la app, el iPhone que crea el espacio puede generar un código para otro iPhone o eliminar los datos remotos. Los iPhones invitados no pueden borrar el espacio. La eliminación no modifica NUT ni borra el histórico local del iPhone.

## Qué funciona

- Cliente TCP NUT de sólo lectura, sin `upsc`, Python, Docker, bibliotecas externas ni acceso USB/GPIO. NUT y su driver siguen siendo necesarios.
- Varias fuentes NUT y varios UPS; descubrimiento con `LIST UPS`, lectura con `LIST VAR`. Opcionalmente selecciona `ups_keys`.
- Ciclos con pausa de 3 segundos después de terminar cada consulta; no se superponen. Hasta cuatro servidores en paralelo. El driver NUT determina qué tan reciente es el dato del hardware.
- Envíos ordinarios al backend cada 30 segundos, agrupando los mensajes pendientes. El primer envío al arrancar es inmediato; los eventos de energía y conectividad adelantan un envío. Los fallos conservan los mensajes y respetan el backoff y `Retry-After` existentes. El intervalo no bloquea las lecturas locales.
- Última lectura persistente, con fecha original y estado `stale` cuando una lectura falla. Nunca se inventan valores faltantes ni se hace contar hacia atrás la autonomía.
- Eventos: entrada en batería, regreso de energía, batería baja, caída de tensión, servidor NUT inaccesible y reconectado. Los de conectividad requieren dos fallos consecutivos por defecto y no se repiten en cada sondeo. Una falla de autenticación no se presenta como pérdida de energía o servidor inaccesible.
- Batería baja se determina por `battery.charge` y el umbral `battery.charge.low` válido del UPS; si falta el umbral, se usa el porcentaje configurado del agente. Runtime, `LB` o `FSD` por sí solos no generan esta alerta. Los eventos incluyen el porcentaje observado y el umbral utilizado.
- Potencia medida cuando existe `ups.realpower`; en otro caso, estimación marcada como tal a partir de potencia nominal y carga.
- Cola persistente para envío HTTPS, reintentos con espera progresiva, identidad estable e identificadores para deduplicar. Una recepción HTTP 200 sin confirmación explícita **no borra** la cola.
- Diario incremental con checksum y sincronización a disco; checkpoint atómico cada 4 MiB. Recupera una escritura final incompleta y conserva un archivo corrupto para reparación, sin resetearlo silenciosamente. Sólo un proceso puede utilizar el mismo directorio.
- Servicio systemd y binarios Linux ARM64, AMD64 y ARMv7 sin dependencias dinámicas de Go/libc.

No hay comandos de control, cambios de configuración del UPS, apagados, servidor web público ni puertos de escucha. Todas las conexiones del agente son salientes. Los servidores locales simulados existen únicamente en las pruebas.

## Alcance y compatibilidad

Objetivo inicial: Raspberry Pi 4 con Raspberry Pi OS / Debian 12 ARM64. Se incluye compilación Linux AMD64 y ARMv7 (por ejemplo Pi 3/4 con sistema de 32 bits; no ARMv6/Pi antiguos). Las pruebas automatizadas se ejecutaron en macOS; **el servicio y los binarios Linux todavía necesitan una prueba física en Raspberry Pi**. No se declara soporte probado para NAS, Windows o macOS como servicio.

Go se requiere sólo para compilar desde el código. El usuario que recibe el paquete binario no necesita instalar Go ni un backend propio. Para HTTPS se utiliza el almacén de certificados CA del sistema (`ca-certificates` en Debian); el usuario no necesita generar un certificado de cliente.

## Compilar y probar

Desde esta carpeta, con Go 1.24 o superior. Para empaquetar el backend se utiliza Python 3; no es una dependencia de ejecución en la Raspberry Pi:

```sh
go test -race ./...
go vet ./...
sh scripts/build.sh
sh scripts/package.sh
```

Los binarios, paquetes y `SHA256SUMS` quedan en `dist/`. Cada paquete contiene la configuración de ejemplo, el instalador, el servicio y esta guía. Los checksums detectan archivos alterados, pero no sustituyen una firma digital del distribuidor.

## Primera prueba sin instalar un servicio

En el equipo Linux, extrae el paquete de su arquitectura. Copia `config.example.json` como `config.local.json`. Configura:

- `state_dir`: una ruta absoluta privada y escribible; por ejemplo `/home/pi/nut-ups-agent-state` si ése es tu usuario. La carpeta debe tener permisos 0700 o 0750.
- `sources[].host`: `127.0.0.1` si el agente corre junto a NUT; la IP o nombre del servidor si corre en otro equipo.
- `sources[].id`: nombre estable elegido por ti. No cambiarlo para renombrar una fuente; utiliza `name`.
- `backend.url`: vacío para esta primera prueba local; el modo remoto se activa únicamente mediante configuración explícita o vinculación.
- `upload_seconds`: 30 por defecto, independiente de `poll_seconds` (3). Se admiten valores de 1 a 30 para mantener el latido por debajo del umbral de disponibilidad del backend. Si falta el campo en una instalación anterior, se usa 30 sin reescribir el archivo.

```sh
./nut-ups-agent-linux-arm64 -config ./config.local.json -check-config
./nut-ups-agent-linux-arm64 -config ./config.local.json -once
./nut-ups-agent-linux-arm64 -config ./config.local.json -status
./nut-ups-agent-linux-arm64 -config ./config.local.json
```

El modo `-once` guarda el resultado y, si existe backend, intenta un envío. Sin backend puede terminar correctamente aunque NUT no responda: consulta `-status` para ver el resultado de lectura, su fecha y si está desactualizado. `-check-config` valida el JSON y sus límites, sin probar red ni leer secretos. `Ctrl+C` detiene el agente normalmente.

**Ejemplo opcional para el entorno de desarrollo de este proyecto**, no configuración obligatoria de producción:

```json
{
  "id": "office-nut",
  "name": "Office",
  "host": "192.168.1.130",
  "port": 3493,
  "ups_keys": ["ups_office"]
}
```

Con la lectura de referencia: batería 100%, carga 5%, nominal 810 W → estimación 40.5 W; autonomía 11430 segundos. No se presupone que otros UPS publiquen esos campos o esa potencia.

## Actualizar una instalación existente

Extraer el paquete 0.4.0 de la arquitectura correcta y ejecutar:

```sh
sudo sh scripts/install-and-start.sh ./nut-ups-agent-linux-arm64
sudo systemctl status nut-ups-agent --no-pager -l
sudo journalctl -u nut-ups-agent -n 10 --no-pager
```

El instalador conserva configuración, estado y vinculación existentes; no hay que volver a emparejar. El inicio debe informar `version=0.4.0`, `poll_seconds=3` y `upload_seconds=30`. Actualizar primero el backend 0.4.0 (schema 4) antes de distribuir el nuevo flujo de instalación. Antes de instalar, verificar el paquete con `SHA256SUMS`.

Los lotes reducen las solicitudes HTTPS, no la cantidad de lecturas que procesa el backend. El histórico remoto conserva una lectura por minuto y lecturas adicionales para eventos, durante siete días. No se promete retención de todas las muestras de tres segundos en el servidor.

## Instalar en Raspberry Pi / Debian

Desde la carpeta extraída, después de revisar el binario y su checksum:

```sh
sudo sh scripts/install.sh ./nut-ups-agent-linux-arm64
sudo nano /etc/nut-ups-agent/config.json
sudo -u nut-ups-agent /usr/local/bin/nut-ups-agent -check-config
sudo systemctl enable --now nut-ups-agent
sudo journalctl -u nut-ups-agent -f
```

El instalador crea el usuario de sistema `nut-ups-agent`, instala en `/usr/local/bin`, guarda configuración en `/etc/nut-ups-agent` y datos en `/var/lib/nut-ups-agent`. Conserva una configuración existente. No inicia ni reinicia el servicio automáticamente; tampoco cambia NUT, el firewall o el router. Para aplicar cambios posteriores: `sudo systemctl restart nut-ups-agent`.

Consulta local:

```sh
sudo -u nut-ups-agent /usr/local/bin/nut-ups-agent -status
```

No ejecutes dos agentes apuntando al mismo `state_dir`. Mantén el equipo y la red local alimentados por el UPS si quieres observar cortes eléctricos.

## Credenciales y envío opcional

La lectura anónima es el modo inicial. Si el servidor exige autenticación, añade `username` y `password_file` a esa fuente. El archivo de contraseña debe estar fuera del proyecto, ser accesible al usuario del servicio y tener permisos 0600 o 0640. Se elimina sólo el salto de línea final; se conservan espacios que formen parte de la contraseña.

```json
{
  "id": "local-nut",
  "name": "Office",
  "host": "127.0.0.1",
  "port": 3493,
  "username": "upsapp",
  "password_file": "/etc/nut-ups-agent/nut-password.secret"
}
```

NUT TCP no se cifra en este piloto: úsalo dentro de una red confiable. La contraseña NUT nunca se incluye en el diario, checkpoint, cola, estado mostrado ni envíos HTTPS. Tampoco se envían el host privado, el usuario NUT, números de serie ni variables arbitrarias. Los nombres de fuente y claves de UPS sí forman parte de la telemetría; usa nombres que quieras compartir con el servicio remoto opcional.

Para activar el piloto, el operador genera un código temporal siguiendo la [guía del backend](backend/README.md). Detén el servicio antes de vincularlo; mantén `backend.url` vacío y ejecuta:

```sh
sudo systemctl stop nut-ups-agent
sudo -u nut-ups-agent /usr/local/bin/nut-ups-agent \
  -pair-url https://ups.omstudioit.com/api/v1 -pair-code XXXX-XXXX-XXXX-XXXX
sudo systemctl start nut-ups-agent
```

Usa el código real, válido una sola vez durante 10 minutos. La vinculación guarda un token privado separado dentro de `state_dir`; el archivo `backend-connection.json` contiene sólo su referencia y la URL. Si la respuesta se pierde, genera otro código. Volver a vincular el mismo agente rota su token; no borres su estado para hacerlo. Durante el piloto no hay registro público ni QR.

Alternativamente configura manualmente `backend.url` con el endpoint HTTPS completo de ingestión y `backend.token_file` con un token válido del agente en un archivo separado, también 0600/0640. La configuración explícita tiene prioridad sobre una vinculación guardada. No introduzcas credenciales en la URL. Se valida el certificado TLS, no se siguen redirecciones y no hay opción para desactivar esa validación. El token se relee para permitir rotación sin reiniciar. El permiso `allow_http_loopback` sólo habilita HTTP a una IP loopback para pruebas locales; nunca a servidores LAN o internet.

El [contrato del backend](docs/backend-contract.md) describe el protocolo implementado. No basta con apuntar a una página web o webhook arbitrario: se requiere la API con confirmación durable.

## Límites de almacenamiento y eventos

La cola conserva por defecto hasta **4096 mensajes o 16 MiB**, lo que ocurra primero. Con un mensaje cada 3 segundos son aproximadamente 3 h 25 min si el tamaño no alcanza antes el límite de bytes. No es una promesa de retención: depende del tamaño y frecuencia efectiva. Al llenarse se descartan primero mensajes sin eventos; si todos contienen eventos también pueden perderse los más antiguos. El contador `dropped_messages_total` hace visible esa pérdida. La última lectura se conserva aparte de esa retención; los últimos 256 eventos quedan disponibles localmente.

Los datos locales usan permisos privados, **no cifrado en reposo**. Para respaldarlos de manera consistente detén el servicio y copia `state.json` y `journal.bin` juntos. No borres únicamente uno de ellos. Una falla de disco detiene el agente para no seguir como si las observaciones estuvieran guardadas.

Se soportan hasta 16 fuentes y 64 UPS por fuente; el caché de última lectura se limita a 1024 dispositivos, retirando primero los desactualizados más antiguos. Al cambiar host, puerto o ID se usa una identidad distinta; los mensajes históricos pendientes mantienen su identidad original. Reinicios o huecos de lectura pueden hacer incierto el instante físico de un evento: se conserva la fecha de observación y se marca `timing_uncertain`.

## Prueba física pendiente

1. Verificar arquitectura y ejecutar `-once` / `-status` en la Raspberry Pi.
2. Ver que aparecen los UPS reales y sólo sus métricas disponibles.
3. Detener/reiniciar el agente: lecturas y mensajes pendientes deben conservarse.
4. Simular falta de conexión a NUT: valores congelados, fecha original y un evento de servidor inaccesible tras el umbral.
5. Restablecer NUT: lectura nueva y un evento de reconexión.
6. En una prueba eléctrica controlada por el usuario, observar OL → OB → OL y batería baja si se puede probar con seguridad. El agente no provoca esos cambios.
7. Probar pérdida de internet, reenvío y deduplicación contra el backend del piloto. Después integrar la app móvil y las notificaciones push.

Referencias: [protocolo oficial NUT](https://networkupstools.org/docs/developer-guide.chunked/net-protocol.html), [Go](https://go.dev/doc/install), [systemd](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html).
