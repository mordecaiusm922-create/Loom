# Roadmap de Loom

Este documento es el roadmap completo, desglosado a nivel de épica/ticket,
para construir encima de lo entregado en las Fases 0 y 1 (ya implementadas
y con tests en verde en este repo). Cada item tiene: por qué existe, que
archivos toca, criterio de aceptacion, y de que depende. El orden dentro de
cada fase es el orden recomendado de ejecucion, no una lista arbitraria.

Principio rector: Loom se endurece como herramienta standalone antes de
acoplarse fuerte a DevMind. DevMind esta en construccion; el colchon de
seguridad local de Loom (fail-closed, guardrails por-tool, clasificacion de
blast-radius) esta diseñado para sostenerse solo, no para tapar que DevMind
todavia no es confiable.

---

## Direccion (2026-09): Loom como cliente open source de DevMind

Decidido: Loom es **open source (Apache-2.0)** y es el cliente que un
SRE usa a diario; DevMind (closed-source, motor determinista) es el
control plane de pago que decide. Shell nativo del usuario (bash/zsh en
Linux/macOS, pwsh en Windows), ingles por defecto con `es` opcional, y
primer wedge **plan → policy → apply** para Terraform/OpenTofu y K8s.
DevMind ya expone REST + MCP con auth por org, asi que buena parte de la
Fase 3 original ya no esta bloqueada (ver Fase D).

Orden: A (correccion/seguridad, hecha) → B (identidad OSS) → C (wedge
plan→apply, primer release publico) → D (integracion profunda con
DevMind) → E (UX nivel Claude Code, intercalada desde C) → F (segundo
wedge: incidentes y postmortems).

### Fase A — Hecho
A1 `cloud_read` ya no rechaza `--output`/`--format`/`describe-addresses`;
A3 resolucion de ambiente declarado (`sre.environments`), unknown = prod
para cambios de infra; A4 clasificador por argv con suite de evasiones
permanente; A5 rutas de credenciales bloqueadas y redaccion de toda salida
de tool; A2 memoria entre turnos en `loom chat` + `/clear`; A6 tope de
salida con cabeza y cola; A7 session IDs unicos + un `.jsonl` por sesion;
A8 `audit_id` de DevMind en el timeline. Un commit por ticket, cada uno
con su test de regresion.

### Fase B — Identidad OSS (siguiente)
B1 tool `shell` con el shell nativo (alias `powershell`), B2 i18n (en por
defecto, es opcional), B3 `LOOM.md` como contexto de proyecto, B4 LICENSE
Apache-2.0, sacar `loom.exe` del historial, SECURITY.md, goreleaser.

### Fase C — plan → policy → apply
`internal/iac/terraform` (plan -out + show -json, clasificacion por
recurso incl. replace `-/+` sobre recursos stateful), maquina de estados
en `.loom/runs/<id>/`, plan JSON a `/evaluate-change` como
`terraform_plan`, `loom apply` solo del tfplan revisado (verificado por
sha256) y `terraform apply` crudo por shell rechazado localmente, K8s via
`diff` + dry-run de servidor, tabla de riesgo por recurso, exit codes
documentados, GitHub Action que comenta el plan en el PR.

### Fase D — Integracion con DevMind
REST versionado (`/v1`) como transporte de gobernanza (reinterpreta 3.1:
las tools MCP de DevMind devuelven texto para agentes, no un contrato
estructurado); implementacion del break-glass 3.0-(d); aprobacion
REVIEW fuera de la TTY via review_requests/Slack de DevMind (3.4);
outcome ejecutado/verificado reportado por `audit_id` (3.3).

---

## Fase 0 — Hecho (heredado)

Nucleo de ejecucion gobernada: integracion REST con DevMind, evaluacion de
politicas fail-closed, clasificacion de `ChangeType` para Terraform/K8s/
Helm/IAM/secrets/migraciones/llamadas directas a cloud, ruteo de modelo por
tarea, trazas de auditoria, `init`/`doctor`/`config`/`run`/`chat`.

## Fase 1 — Hecho (esta ronda)

Loom re-escopado como herramienta de SRE/Platform Engineering, independiente
de la madurez de DevMind: PowerShell como shell, tools nativas de
investigacion (`k8s_get`/`k8s_describe`/`k8s_logs`/`cloud_read`/
`metrics_query`), config `sre` de primera clase, timeline de incidente
persistido (`.loom/sessions/*.jsonl`), runbooks reutilizables, prompt
acotado a SRE, fix del bug de `update_plan` no registrado.

---

## Fase 2 — Endurecer Loom (sin tocar DevMind)

**Estado: completa.** 2.1, 2.2, 2.3, 2.4, y 2.5 implementados y con tests.
Nota de auditoria: la primera pasada de 2.1/2.2/2.3/2.5 llego con un bug real
en 2.2 -- `SetDryRun`
guardaba la bandera pero `Execute()` nunca la leia, asi que `--dry-run`
ejecutaba cada comando mutante de verdad -- y sin tests nuevos que lo
hubieran atrapado. Ya esta corregido (`powershell` y `write_file` ahora
declaran `IsMutating`, `Execute` corta antes de consultar gobernanza) y
cubierto por `TestDryRunSkipsMutatingToolsAndGovernance` y afines. Cualquier
tool nuevo que mute estado real DEBE declarar `IsMutating`, o quedara
invisible para `--dry-run` de la misma forma.

### 2.1 Exponer MCP al loop del agente — Hecho (endurecido en esta ronda)

Wiring inicial hecho en la ronda de auditoria; en esta ronda se le agrego lo
que le faltaba a `internal/mcp/client.go` para no ser un riesgo en un
incidente real:
- **Timeout por llamada** (`DefaultTimeout` = 15s, configurable via
  `StartStdioServerWithTimeout`): un servidor MCP colgado ya no cuelga la
  sesion entera esperando una linea de respuesta que nunca llega.
- **`Close()` con SIGKILL de respaldo** (`CloseGracePeriod` = 3s): un
  servidor que ignora el cierre de stdin y nunca sale por su cuenta ya no
  cuelga el cierre de la sesion.
- **Degradacion parcial** en `connectMCPServers`: un servidor mal
  configurado o inalcanzable ya no aborta `NewSession` completa -- se
  registra como warning (`Session.MCPWarnings()`) y Loom sigue con los
  tools nativos y el resto de los servidores MCP que si conectaron.
- Cubierto por `internal/mcp/client_test.go` (round-trip contra un servidor
  falso en `sh`, timeout, kill de proceso colgado, cierre limpio del caso
  feliz) y `TestNewSessionDegradesPartiallyWhenAnMCPServerFails`.
**Por qué**: los servidores MCP conformes al spec rechazan `tools/list` antes
de completar `initialize`; sin ese lifecycle, Loom solo funcionaba contra
dobles de prueba permisivos.
**Hecho**: `StartStdioServer` y `StartStdioServerWithTimeout` ahora envían
`initialize` con `protocolVersion`, `capabilities` y `clientInfo`, esperan su
respuesta y luego envían `notifications/initialized` antes de devolver el
cliente listo. Un error o timeout de ese handshake aborta el subproceso y no
expone un cliente parcialmente inicializado.
**Cubierto por**: `TestDiscoverToolsRoundTrips` exige el handshake antes de
`tools/list`; `TestStartFailsWhenInitializeTimesOut` verifica que un servidor
que no responde a `initialize` falle rápido.
### 2.2 Modo `--dry-run` — Hecho (corregido en esta auditoria)
**Por que**: ensayar un runbook o una migracion sin que ninguna llamada
mutante se ejecute de verdad, ni siquiera si gobernanza la aprobaria. Util
para entrenar a alguien nuevo on-call, o para revisar el plan del modelo
antes de dar luz verde en un cambio de alto riesgo.
**Toca**: `cmd/loom/main.go` (flag `--dry-run` en `run`/`chat`),
`internal/tools/tools.go` (en `Execute`, si `dryRun` y el tool es mutante,
cortocircuitar a `"[dry-run] habria ejecutado: <descripcion>"` sin llamar
`Run` ni a gobernanza), `internal/agent/loop.go` (propagar el flag a
`Session`).
**Criterio de aceptacion**: con `--dry-run`, ningun proceso hijo se lanza
para `powershell`/`k8s_*` mutantes ni `cloud_read` bloqueado por verbo;
`k8s_get`/`k8s_describe`/`k8s_logs`/`metrics_query`/`cloud_read` de solo
lectura SI se ejecutan de verdad (dry-run no debe impedir que el modelo
siga investigando). Test que verifica que el contador de `exec.Command`
mutante es 0 con la flag activa.
**Depende de**: nada.
**Tamaño**: S.

### 2.3 Resume de sesion — Hecho
**Por que**: un handoff de guardia no deberia perder contexto. Poder
retomar un incidente desde su `.jsonl` en una sesion nueva.
**Toca**: nuevo `internal/agent/resume.go` (parsear `.loom/sessions/<id>.jsonl`
a un resumen legible), `cmd/loom/main.go` (`loom run --resume <session-id>
"<nuevo contexto>"`).
**Criterio de aceptacion**: `loom run --resume sess-1234 "sigue subiendo el
error rate"` inyecta como contexto inicial un resumen de las tools llamadas
y sus decisiones/outcomes de `sess-1234`, no el archivo crudo (para no
gastar contexto de forma innecesaria).
**Depende de**: 2.5 (el formato del `.jsonl` deberia estar ya estabilizado
antes de escribir un parser sobre el).
**Tamaño**: M.

### 2.4 Salida estructurada para tools nativas — Hecho
**Por que**: hoy `k8s_get -o json` devuelve el JSON crudo de kubectl, que el
modelo tiene que re-parsear cada vez, gastando contexto en investigaciones
de varios pasos.
**Hecho**: nuevo `internal/tools/summarize.go`. `k8s_get` condensa
`output: "json"` a una tabla (NAME/READY/STATUS/RESTARTS/AGE/NODE para
pods; READY/UP-TO-DATE/AVAILABLE para deployments/replicasets/statefulsets/
daemonsets; STATUS/ROLES/VERSION para nodes; TYPE/CLUSTER-IP/PORTS para
services; TYPE/REASON/OBJECT/MESSAGE/COUNT para events) con `raw: true`
como escape hatch. `metrics_query` condensa resultados `vector`/`scalar` de
Prometheus/Grafana a una tabla METRIC/VALUE y de Datadog a METRIC/SCOPE/
LATEST; los resultados `matrix`/rango se devuelven completos a proposito
-- condensarlos perderia la serie temporal que los hace utiles. Un tipo de
recurso o una respuesta de error no reconocidos caen de vuelta al JSON
crudo sin fallar. `cloud_read` queda deliberadamente sin condensar: el
formato de salida de aws/gcloud/az varia demasiado por subcomando como para
normalizarlo de forma generica y segura; ver Fase 4 si esto se vuelve un
problema real de contexto.
**Cubierto por**: `internal/tools/summarize_test.go`, con fixtures
realistas (PodList, Deployment individual, NodeList con roles, respuestas
vector/matrix/error de Prometheus, respuesta ok de Datadog, kind
desconocido, JSON invalido, lista vacia).
**Depende de**: nada.
**Tamaño**: M.

### 2.5 Accounting de costo/latencia por sesion — Hecho
**Por que**: `cost_input_per_mtok`/`cost_output_per_mtok` ya se configuran
por proveedor pero no se usan en ningun lado.
**Toca**: `internal/providers/*.go` (devolver tokens de input/output usados
por llamada), `internal/agent/loop.go` (acumular y loguear al final de la
sesion), `internal/agent/events.go` (nuevo `EventType` para el resumen de
costo).
**Criterio de aceptacion**: al terminar `loom run`, se imprime (y se
persiste en el `.jsonl`) el costo estimado y la duracion total de la
sesion.
**Depende de**: nada.
**Tamaño**: S.

---

## Fase 3 — Integracion con DevMind (bloqueado por la madurez de DevMind)

No arrancar 3.1–3.4 hasta que DevMind tenga: (a) un contrato de
`ChangeType`/decision estable, (b) un servidor MCP real expuesto, y (c) un
contrato de break-glass/flag-org consultable (definido en 3.0 abajo).
Empezar antes significa reescribir esta fase cuando el contrato cambie.
3.0 (diseño) está decidido; 3.1–3.4 siguen bloqueadas.

### 3.0 Pre-trabajo: relacion entre el bypass local y el break-glass de DevMind — DECIDIDO

**Estado: decidido (diseño, sin codigo en este ticket).**

**Contexto**: Loom tiene `LOOM_UNSAFE_DISABLE_GOVERNANCE=1`, un bypass total
y silencioso: `NewSession` (`internal/agent/loop.go`) reemplaza el
`governance.Engine` por un `NoopEngine` local que devuelve `ALLOW` a todo,
sin avisar a ningun servidor. Peor: el `.jsonl` local registra esos eventos
con `Engine: "policy"/"infra"`, indistinguibles de aprobaciones reales de
DevMind. DevMind, del otro lado, ya tiene break-glass por llamada
(`break_glass=true` + justificacion obligatoria, solo BLOCK/REVIEW, nunca
ESCALATE, logueado en `break_glass_log`) y planea una bandera org
server-side para PROHIBIR el break-glass. El dia que Loom hable con DevMind
por MCP, esa bandera no frena a un usuario que exporta la variable local y
simplemente nunca llama al servidor. Hay que resolverlo en el diseño ANTES
de escribir el cliente MCP (3.1).

**Nota de procedencia (regla 5)**: el ticket describia una seccion "Opciones
a evaluar (a, b, c)" que NO existia en este archivo al momento de decidir
(verificado por busqueda: sin "3.0" ni "break-glass" en ROADMAP.md). Las
opciones (a)–(c) abajo son la reconstruccion fiel del problema planteado en
el ticket, no una cita del archivo; (d) es la propuesta nueva.

**Limite honesto del threat model (vale para las tres opciones)**: Loom
corre en la maquina del usuario, que controla env vars, config y binario.
Ningun check client-side puede IMPEDIR un bypass a un cliente deshonesto
(recompilar sin esas 5 lineas lleva minutos). La garantia alcanzable no es
prevencion criptografica sino triple: (1) los clientes honestos obedecen la
politica del servidor, (2) todo bypass es declarado, justificado y
auditado, (3) el enforcement real vive donde el usuario no es trusted
(ejecucion server-side o credenciales de corta duracion brokeradas por
DevMind — fuera del alcance de 3.x, anotado abajo como futuro, no como
promesa de este diseño).

**Opciones evaluadas**:
- **(a) Eliminar el bypass** (quitar la env var y `NoopEngine`; gobernanza
  siempre obligatoria). **Rechazada**: rompe el uso legitimo sin red
  (desarrollo y tests — `internal/agent/loop_test.go` la usa — e incidentes
  donde el control plane no responde) y es teatro de seguridad: no impide
  nada contra binario modificado, empuja a workarounds peores y encima le da
  a la org una falsa confianza ("prohibido" que no prohibe).
- **(b) Bypass ruidoso pero local** (banner, justificacion, marca en el
  `.jsonl`). **Rechazada como solucion completa, valida como pieza**: el
  ruido local no es control org-level; el servidor sigue sin enterarse y el
  caso del ticket (org que prohibe, usuario que bypassea igual) queda intacto.
- **(c) Break-glass mediado por servidor** (la env var solo SOLICITA;
  DevMind autoriza/deniega segun la flag org con un grant de corta
  duracion; sin red, fail-closed). **Correcta en direccion, ingenua en su
  forma pura**: fail-closed-sin-red deja a Loom muerto justo en el incidente
  donde mas se necesita (el patron AWS real existe precisamente para cuando
  el control plane no responde), y "el servidor prohibe" no frena a un
  cliente que simplemente no pregunta — hay que decirlo o mentimos en el
  diseño.
- **(d) Break-glass declarado con doble modo + paridad ESCALATE +
  reconciliacion (propuesta nueva, adoptada; superset de b+c)**.

**Decision (d)**:
1. Separar dos conceptos hoy conflados en una sola env var: **modo DEV**
   (tests/desarrollo local, sin infra real; via explicita de desarrollo,
   nunca contra prod) vs **break-glass PROD** (emergencia con infra real;
   unico camino con gobernanza desactivada contra produccion).
2. Eliminar el Noop silencioso: deprecar
   `LOOM_UNSAFE_DISABLE_GOVERNANCE=1` y reemplazarla por un break-glass
   declarado que exige justificacion no vacia, confirmacion interactiva
   (`--non-interactive` lo deniega fail-closed, igual que REVIEW/ESCALATE en
   4.3), y marca la sesion entera como BREAK-GLASS (banner, `Engine:
   "break-glass"` en cada evento en vez del `"policy"/"infra"` engañoso
   actual, justificacion persistida en el `.jsonl`).
3. **Mediacion server cuando hay conectividad**: Loom consulta la flag org
   (cache local con TTL); si la org prohibe, se deniega fail-closed para el
   cliente honesto; si permite, grant de corta duracion con log dual
   (`break_glass_log` en server + `.jsonl` local) y revision post-incidente
   obligatoria.
4. **Modo offline accountable cuando NO hay conectividad** (o cache
   vencida/ausente): permitido SOLO con justificacion + confirmacion + log
   local marcado UNRECONCILED + reenvio best-effort al reconectar (mismo
   principio que 3.3: fail-open-en-disponibilidad, fail-closed-en-decision).
   La garantia contra cliente deshonesto es deteccion/atribucion en la
   reconciliacion, no prevencion — dicho explicitamente.
5. **Paridad ESCALATE**: ningun modo (ni break-glass autorizado ni offline)
   puede overridear ESCALATE — cierra el hueco actual donde el Noop local es
   MAS permisivo que el break-glass server-side, y respeta el invariante ya
   decidido en DevMind.
6. **Limite documentado**: el enforcement criptografico real (DevMind
   brokeando credenciales efimeras o ejecutando server-side) queda como
   trabajo futuro fuera de 3.x; este diseño no lo promete.
**Por que (d) y no las otras**: (a) miente sobre lo que puede garantizar y
rompe lo legitimo; (b) no mueve la aguja org-level; (c) pura deja sin
herramienta el incidente sin red y calla el limite del cliente deshonesto.
(d) conserva lo rescatable de cada una — de (b) el ruido local obligatorio,
de (c) la mediacion server y la flag org para honestos — y agrega lo que
faltaba: split DEV/PROD, offline accountable con reconciliacion, paridad
ESCALATE, y el limite del trust boundary por escrito para que nadie lea la
flag org como una prohibicion criptografica que no es.
**Consecuencias para 3.1–3.4**: 3.1 ahora exige de DevMind, ademas de (a) y
(b), **(c) contrato break-glass**: lectura de flag org `allow_break_glass`
(con TTL documentado), RPC de grant con justificacion requerida, y
`break_glass_log` consultable para reconciliacion. 3.3 reutiliza el mismo
transporte para el reenvio offline.
**Criterio de aceptacion (este ticket, diseño)**: esta seccion existe y
3.1–3.4 la referencian; ningun codigo cambia. El ticket de implementacion
debera probar que algo LEE cada flag/env nuevo (regla aprendida del bug
`--dry-run`), que offline exige justificacion, que ESCALATE nunca se
overridea, y que el evento lleva `Engine: "break-glass"`.
**Depende de**: nada en codigo; de DevMind solo para implementar (c).
**Tamaño**: S (diseño, hecho aqui); implementacion futura M.
**Toca (futuro, NO en este ticket)**: `internal/governance/*` (metodo
break-glass en la interfaz), `internal/agent/loop.go`, `cmd/loom/main.go`,
`internal/config/config.go`, sink de reconciliacion (reusa 3.3), README
(deprecacion de la env var vieja).

### 3.1 Cliente `governance.Engine` sobre MCP
**Por que**: reemplazar la implementacion REST actual por MCP, sin tocar el
resto del sistema — `governance.Engine` ya es una interfaz
(`EvaluateAction`/`EvaluateChange`) exactamente para que este swap sea
quirurgico.
**Toca**: nuevo `internal/governance/devmind_mcp.go` implementando la misma
interfaz que `internal/governance/devmind.go` (REST), `cmd/loom/main.go`
(elegir implementacion segun `governance.transport` en config: `"rest"` o
`"mcp"`), sin tocar `internal/tools/tools.go` ni `internal/agent/loop.go`.
**Criterio de aceptacion**: los mismos tests de `tools_test.go` pasan sin
modificacion contra un `fakeEngine` que implementa la interfaz — prueba de
que el boundary aguanta el swap. Config de ejemplo con
`"governance": {"transport": "mcp", ...}`.
**Depende de**: DevMind exponiendo un servidor MCP.
**Tamaño**: M (si el contrato de decision es igual al REST actual) a L (si
cambia).

### 3.2 Politicas compartidas a nivel de organizacion
**Por que**: hoy la config de gobernanza vive en `loom.config.json` por
checkout de proyecto; un equipo necesita una fuente de verdad unica.
**Toca**: `internal/config/config.go` (resolver politicas desde un endpoint
central en vez de/ademas del archivo local), posible cache local con TTL
para no depender de conectividad en cada arranque.
**Criterio de aceptacion**: dos proyectos distintos con el mismo
`org_policy_id` resuelven la misma politica efectiva sin copiar/pegar
config.
**Depende de**: 3.1 y de que DevMind (u otro servicio) exponga ese
endpoint.
**Tamaño**: M.

### 3.3 Almacenamiento de auditoria server-side
**Por que**: `.loom/sessions/*.jsonl` es local por diseño (funciona sin
depender de nada externo); un equipo necesita un store compartido donde
DevMind tambien pueda escribir sus propias decisiones para correlacionar.
**Toca**: `internal/agent/loop.go` (sink adicional ademas del archivo
local, nunca en reemplazo — el local sigue siendo el fallback si el sink
remoto falla), nuevo `internal/audit/remote.go`.
**Criterio de aceptacion**: con el sink remoto configurado y caido, la
sesion sigue funcionando (solo el local persiste) — mismo principio
fail-open-en-disponibilidad/fail-closed-en-decision que ya rige el resto
del sistema.
**Depende de**: 3.1, y de que exista el servicio remoto.
**Tamaño**: M.

### 3.4 Flujos de aprobacion humana fuera de la TTY
**Por que**: hoy REVIEW/ESCALATE piden `y`/`n` en la misma terminal donde
corre Loom. Un incidente real necesita que otra persona (no la que esta al
teclado) pueda aprobar, ej. desde Slack o PagerDuty.
**Toca**: `internal/governance/devmind.go` o `devmind_mcp.go` (soporte para
una decision `PENDING` que se resuelve async), `internal/agent/loop.go`
(esperar la resolucion con timeout configurable en vez de bloquear en
`stdin`), nueva integracion de notificacion (Slack webhook como primer
canal).
**Criterio de aceptacion**: una accion en REVIEW notifica a un canal de
Slack configurado y la sesion queda bloqueada esperando la respuesta hasta
timeout; timeout sin respuesta se resuelve igual que hoy un fallo de
gobernanza — fail-closed.
**Depende de**: 3.1.
**Tamaño**: L.

---

## Fase 4 — Control plane de equipo/enterprise

### 4.1 Visibilidad de flota multi-agente
**Por que**: saber que sesiones de Loom estan corriendo, por quien, contra
que ambiente, en tiempo real — no solo el `.jsonl` post-hoc de cada una.
**Toca**: requiere un servicio nuevo fuera de este repo (un backend que
reciba heartbeats de cada sesion activa) + un cliente ligero en
`internal/agent/loop.go` que reporte.
**Depende de**: 3.3 (reusa el mismo transporte de auditoria remota).
**Tamaño**: L.

### 4.2 UI de autoria de politicas sobre DevMind
**Por que**: reemplazar politicas escritas a mano por una interfaz que un
SRE lead pueda editar sin tocar codigo.
**Depende de**: 3.2. Es un proyecto de frontend separado, no vive en este
repo Go.
**Tamaño**: XL (proyecto aparte).

### 4.3 Integracion con GitHub Actions — Hecho
**Por qué**: correr `loom run --task ...` como paso gateado en un pipeline,
para que un cambio de infra pase por la misma gobernanza en CI que en la
laptop de un SRE.
**Hecho**: `loom run --non-interactive` mantiene `BLOCK` como fallo y trata
`REVIEW`/`ESCALATE` como fallo fail-closed, sin esperar stdin ni aprobarlos.
El workflow `.github/workflows/loom-example.yml` compila Loom y revisa el
diff Terraform de un pull request como check gateado.
**Cubierto por**: `TestNonInteractiveReviewNeverExecutesTool` comprueba que
REVIEW no llama la tool; README documenta la flag y el workflow de ejemplo.

### 4.4 SSO/RBAC por tipo de tarea y ambiente
**Por que**: no todos deberian poder correr `security_review`/
`incident_response` contra produccion.
**Toca**: requiere un directorio de identidad (fuera de este repo) +
`internal/agent/loop.go` resolviendo el `agentID` real desde una sesion
autenticada en vez de `LOOM_AGENT_ID`.
**Depende de**: 4.1 (mismo backend de control plane).
**Tamaño**: L.

---

## Como usar este documento

Cada item de la Fase 2 es independiente entre si — se pueden tomar en
cualquier orden sin bloquearse mutuamente, salvo 2.3 que depende de 2.5.
Recomendado: 2.2 (`--dry-run`) primero por ser el mas chico y el que mas
valor inmediato da para ensayar runbooks sin riesgo, despues 2.5, 2.1, 2.4,
2.3 en ese orden.

La Fase 3.0 (diseño) esta decidida; 3.1–3.4 siguen bloqueadas hasta que
DevMind confirme (a) contrato `ChangeType`/decision estable, (b) servidor
MCP real, y (c) contrato break-glass/flag-org de 3.0 — no vale la pena
empezar 3.1 antes de eso, se reescribiria.

La Fase 4 depende de decisiones de producto (que backend de control plane,
que proveedor de SSO) que estan fuera del alcance de este repo.
