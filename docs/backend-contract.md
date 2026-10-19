# Contrato de ingestión v1 — agente y backend del piloto

Implementado en el emisor Go y en el [backend PHP/SQLite](../backend/README.md) bajo `https://ups.omstudioit.com/api/v1/ingest`, con pruebas locales y por HTTPS. La vinculación del agente usa códigos temporales; todavía no hay emparejamiento móvil ni APNs. Las páginas de soporte/privacidad conservan su propósito.

## Transporte e identidad

El agente hace POST al endpoint HTTPS exacto configurado, sin redirecciones. Cabeceras:

```http
Content-Type: application/json
Authorization: Bearer <token individual del agente>
Idempotency-Key: <SHA-256 del agent_id y la lista ordenada de message IDs>
```

`agent_id` es un UUID generado una sola vez y conservado en disco. **No autentica por sí mismo:** el backend valida el token, su revocación, el agente autorizado y el usuario propietario antes de aceptar datos. Nunca confiar en un `agent_id` recibido para decidir la cuenta. La vinculación genera ese token y el agente lo guarda en un archivo privado.

`source_id` es SHA-256 de un array JSON `[id_configurado, host_normalizado, puerto]`. `device_id` es SHA-256 de `[source_id, ups_key]`. Los hashes evitan incluir el host privado en la petición; no son un mecanismo de autenticación. Cambiar el endpoint del NUT crea otra identidad. Una descripción o modelo comercial no identifica un UPS.

## Cuerpo

```json
{
  "schema_version": 1,
  "agent_id": "UUID",
  "messages": [
    {
      "schema_version": 1,
      "agent_id": "UUID",
      "id": "MESSAGE-UUID",
      "sequence": 1,
      "recorded_at": "2026-10-01T12:00:00Z",
      "sources": [
        {
          "source_id": "SOURCE-HASH",
          "name": "Office",
          "connection": "reachable",
          "discovered_ups": ["ups_office"]
        }
      ],
      "readings": [
        {
          "device_id": "DEVICE-HASH",
          "source_id": "SOURCE-HASH",
          "ups_key": "ups_office",
          "observed_at": "2026-10-01T12:00:00Z",
          "variables": {
            "ups.status": "OL",
            "battery.charge": "100",
            "ups.load": "5",
            "ups.realpower.nominal": "810"
          },
          "power_watts": 40.5,
          "power_estimated": true
        }
      ],
      "events": [],
      "dropped_messages_total": 0
    }
  ]
}
```

Los valores son un ejemplo del entorno de desarrollo, no constantes de producción. Pueden faltar lecturas/métricas; los arrays sin contenido también pueden ser `null`. `power_watts` se omite cuando no se puede calcular; `power_estimated` distingue estimación y medición.

Cada lectura sólo permite estas variables: `ups.status`, `battery.charge`, `battery.charge.low`, `battery.charge.warning`, `ups.load`, `battery.runtime`, `battery.voltage`, `battery.voltage.nominal`, `input.voltage`, `input.voltage.nominal`, `output.voltage`, `ups.realpower`, `ups.realpower.nominal`, `device.mfr`, `device.model`, `ups.mfr`, `ups.model`, `ups.temperature`. Valores máximos de 256 bytes. El backend debe volver a validar y filtrar el contenido; una lista permitida en el agente no reemplaza su propia validación.

`connection` puede ser `reachable`, `unavailable` o `read_error`. `error_code` es un identificador fijo, nunca el error crudo del servidor. `discovered_ups: []` significa descubrimiento exitoso vacío; `null` significa que no hubo inventario válido. `unavailable_ups` lista dispositivos cuya lectura individual falló. Un servidor accesible puede tener un driver/dispositivo sin datos.

Cuando existe caché desactualizado, `last_known_readings` incluye esas lecturas **con su fecha original**. El backend debe conservarlas como último estado, sin marcarlas en vivo. Evita que la primera vinculación después de una interrupción pierda la última lectura aunque el mensaje original haya salido de la cola.

Eventos: `on_battery`, `power_restored`, `low_battery`, `voltage_drop`, `nut_server_unavailable`, `nut_server_reconnected`. Cada uno tiene `id`, `kind`, `source_id`, `observed_at`; los de UPS también `device_id`. La reconexión puede incluir `disconnected_seconds`. `timing_uncertain` indica que no puede atribuirse el cambio al instante físico exacto.

## Confirmación durable obligatoria

Responder 2xx con JSON:

```json
{"acknowledged_ids":["MESSAGE-UUID"]}
```

Confirmar **sólo después** de guardar de manera durable las lecturas y eventos, o de verificar que ya estaban guardados. Usar restricciones únicas por `(agente_autenticado, message_id)` y por `event_id` dentro del mismo agente. Puede repetirse una petición tras perderse su respuesta; no duplicar historial ni enviar otra alerta push. La idempotencia por mensaje sigue siendo obligatoria aunque cambie el agrupamiento de mensajes y, por tanto, la cabecera `Idempotency-Key`.

El emisor conserva todos los mensajes no confirmados, acepta confirmaciones parciales y rechaza IDs desconocidos/repetidos. Un 204 vacío o un 200 sin IDs no elimina datos. Tamaño máximo de recibo: 64 KiB. Hasta 100 mensajes por petición y objetivo de 512 KiB; un único mensaje grande puede exceder ese objetivo, con límite de petición de 8 MiB.

## Orden, desconexión y notificaciones

- `sequence` persiste entre reinicios del mismo agente; los huecos son posibles por límites de cola. El contador de descarte aumenta de forma acumulativa.
- Guardar tanto `observed_at` del agente como una fecha de recepción propia. La hora de una Raspberry Pi puede estar desajustada; no tratarla como autoridad para seguridad o expiración.
- La fuente actual debe aplicar la observación de mayor secuencia, no el orden de llegada de HTTP. Datos atrasados enriquecen historial sin reemplazar estado nuevo. Las fechas de lectura no se actualizan al sincronizarlas.
- Cada petición autenticada sirve como señal de comunicación reciente, pero mensajes antiguos que llegan tras recuperar internet no convierten sus lecturas antiguas en datos vivos. Distinguir comunicación del agente, disponibilidad de NUT y antigüedad de cada UPS.
- La pérdida de comunicación con el agente **no demuestra un corte eléctrico**. El backend debe registrar "agente sin comunicación" por separado, conservando su última lectura.
- Reintentar errores temporales y 429 con espera progresiva y `Retry-After`, hasta 24 horas de espera indicada por el servidor. 401/403 se reintentan como mínimo cada cinco minutos y no borran la cola. Rotar el token o corregir la configuración para recuperarlos.
- La API debe implementar aislamiento entre usuarios, revocación de agentes, límites, borrado/retención y minimización de registros. No registrar cabeceras Authorization ni secretos.
- Las alertas APNs, cuando se implementen, deben salir de eventos deduplicados. No enviar alertas de cortes antiguos como si acabaran de ocurrir; definir una política para eventos atrasados.

## Pasos siguientes

1. Prueba física Raspberry Pi con el backend del piloto; la validación automatizada usa NUT simulado.
2. Flujo móvil de acceso/vinculación/QR, recuperación de credenciales y distribución segura de actualizaciones.
3. Fuente remota opcional en iOS, conservando el modo local actual.
4. APNs y gestión de permisos. Los widgets conservan los límites de actualización impuestos por iOS.
