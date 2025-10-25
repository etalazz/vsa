# Reducir E/S de disco sin perder corrección: VSA en términos simples

##

El Vector–Scalar Accumulator (VSA) admite solicitudes **exactamente** mientras persiste **muchos menos registros, pero más ricos**. El trabajo en disco escala con el número de agregados significativos **I** (donde **I ≪ N**, el total de solicitudes), no con cada solicitud **N**.

---

## Idea central

* Sea **N** = total de operaciones entrantes (muchas son redundantes o se cancelan entre sí).
* Sea **I** = el conjunto mínimo de agregados lógicos que realmente importan para la durabilidad (normalmente **I ≪ N**).

VSA filtra/autoriza solicitudes exactamente usando **(base duradera + Δ en memoria)**, pero solo vacía a disco el **Δ coalescido**; así, el trabajo en disco escala con **I**, no con **N**.

---

## Por qué esto ayuda incluso si “todo se persiste”

* Usted persiste **agregados**, no cada evento transitorio.
* Cada registro de flush resume **miles de operaciones** que netean a un único delta.
* Agrupa muchos agregados por llamada a la BD → las llamadas a BD tienden a **O(I / batch_size)** por intervalo en lugar de **O(N)**.
* Evita la contención en filas calientes y el I/O por solicitud; el hot path es **puramente en memoria, O(1)**, con autorización atómica.

---

## Pequeño ejemplo numérico

* Llegan **1,000,000** operaciones para la llave/clave *K* en un segundo; netean **+200**.
* **Diseño por solicitud:** ~1,000,000 escrituras duraderas (más locks/contención).
* **VSA:** autoriza las 1,000,000 en memoria, luego persiste **un único +200** (o uno por bucket de tiempo).
* **Resultado:** mismo estado final duradero, **órdenes de magnitud menos E/S**.

---

## Sus benchmarks (comparación justa)

* **Hot path de VSA:** **16–20M ops/s** realizando solo **~0.8k–1.3k llamadas/s a BD** sobre 128 llaves (≈ **13k–28k ops por llamada**).
* **Baselines (token/leaky bucket, E/S por solicitud):** **~25–27k ops/s** y **~50–53k llamadas/s a BD**.
  → Usted sigue persistiendo, pero persiste **mucho menos y más rico**.

---

## ¿Qué es el **Δ coalescido**?

El **Δ (delta) coalescido** es el **cambio neto** acumulado **en memoria** para una clave desde el último commit duradero—la única actualización que **resume muchas** operaciones entrantes.

* Conceptualmente:
  `new_persistent_state = old_persistent_state ⊕ Δ`
* Ejemplo: eventos sobre la clave *K*: `+1, +1, −1, +3, −2 → Δ = +2`.
  Autorice cada solicitud exactamente en memoria; **flush Δ = +2 una vez**, no cinco escrituras.

### Requisitos de seguridad (para preservar la exactitud)

* **Autorización atómica** contra **(base duradera + Δ en memoria)**.
* **Flushes idempotentes** (los replays no aplican doble).
* Use agregación **asociativa/conmutativa** para que el orden de llegada no importe.

---

## Corrección: “sin deriva de sobre‑admisión”

Para cualquier tiempo *t*, las operaciones admitidas **A(t)** nunca exceden el presupuesto emitido **B(t)**:
**A(t) ≤ B(t)**.
Los crashes pueden causar **sub‑admisión** temporal (conservadora), pero nunca sobre‑suscripción.

---

## Cuando el beneficio desaparece

Si **debe** almacenar de forma duradera **cada evento** (p. ej., auditoría/event‑sourcing sin coalescencia), la ventaja de persistencia de VSA es irrelevante—use un WAL/log de eventos. VSA apunta a **admisión exacta con E/S duradera mínima**, no al archivado por evento.

---

## Conclusión

Todo lo que **importa** sigue haciéndose duradero, pero no paga en disco (ni en contención) por la avalancha de ruido intermedio. Ese es el beneficio: trabajo duradero **O(I)** con **I ≪ N**, un hot path O(1) y fuertes garantías de corrección.