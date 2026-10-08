# Issabel AI Assistant

Servidor MCP y módulo web de asistencia para Issabel.

## Componentes

- mcp/: servicio Go.
- web/ai-assistant/: módulo web de Issabel.
- packaging/: construcción de RPM e instalación.

## Dependencias

Requiere Issabel y una versión de PBX API compatible, provista por
issabel-framework. Los controladores de PBX API no forman parte
de este repositorio.


## Proveedores compatibles con OpenAI (incluido OpenRouter)

En **Proveedor y modelo**, elegir **Compatible con OpenAI** y completar:

- **URL base**: para OpenRouter, `https://openrouter.ai/api/v1`.
  Usar la base de la API sin `/chat/completions`; sólo se admite HTTPS,
  sin credenciales, parámetros de consulta ni fragmentos en la URL.
- **Modelo**: el identificador exacto del proveedor, incluido su prefijo cuando
  corresponda. Elegir un modelo con soporte de llamadas a herramientas.
- **API key**: la clave del proveedor. Es obligatoria al guardar y permanece
  cifrada localmente, separada por usuario.

Guardar antes de pulsar **Probar conexión**. Para este proveedor la prueba hace
una inferencia mínima con el modelo guardado y puede consumir tokens; no envía
información ni herramientas de la PBX. El listado de modelos es opcional: si el
proveedor no implementa `/models`, se puede escribir el nombre manualmente y
seguir usando el asistente. Un fallo al cargar sugerencias no impide guardar.

El adaptador utiliza Chat Completions con autenticación Bearer y conserva las
rondas de herramientas durante cada solicitud. Las operaciones sobre la PBX
siguen sujetas a los permisos y aprobaciones habituales. Las redirecciones HTTP
no se siguen. No se admiten cabeceras personalizadas, opciones de routing ni
streaming de tokens. OpenAI nativo sigue utilizando Responses; Anthropic y
Gemini mantienen sus adaptadores actuales.

La API `/v1/provider` acepta `provider: "openai_compatible"` y `base_url`, además
de `model` y `api_key`. Su respuesta incluye `base_url` y sólo información
enmascarada de la clave. Las configuraciones anteriores no necesitan migración.

Referencias: [OpenRouter Quickstart](https://openrouter.ai/docs/quickstart) y
[soporte de herramientas](https://openrouter.ai/docs/guides/features/tool-calling).

## Diagnóstico del proveedor

Para consultar errores y actividad del proveedor desde el servidor con
`journalctl`, ver [Diagnóstico de proveedores compatibles con OpenAI](packaging/README.md#diagnóstico-de-proveedores-compatibles-con-openai).
Incluye seguimiento en vivo, búsqueda por `diagnostic_id` e interpretación de
estados HTTP, tiempos de espera y respuestas vacías.
