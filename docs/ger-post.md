# Platten‑I/O reduzieren, ohne Korrektheit zu verlieren: VSA einfach erklärt

##

Der Vector–Scalar Accumulator (VSA) lässt Anfragen **exakt** zu, während er **weit weniger, aber aussagekräftigere** Datensätze dauerhaft speichert. Die Plattenarbeit skaliert mit der Anzahl der bedeutenden Aggregate **I** (wobei **I ≪ N**, die Gesamtzahl der Anfragen), nicht mit jeder einzelnen Anfrage **N**.

---

## Kernidee

* Sei **N** = Gesamtzahl der eingehenden Operationen (viele sind redundant bzw. heben sich gegenseitig auf).
* Sei **I** = die minimale Menge logischer Aggregate, die für die Dauerhaftigkeit tatsächlich relevant ist (typischerweise **I ≪ N**).

VSA schaltet Anfragen exakt gegen **(dauerhafte Basis + Δ im Speicher)**, schreibt aber nur das **koaleszierte Δ** dauerhaft weg—dadurch skaliert die Arbeit auf der Platte mit **I**, nicht mit **N**.

---

## Warum das hilft, selbst wenn „alles persistiert wird“

* Sie persistieren **Aggregate**, nicht jedes flüchtige Ereignis.
* Jeder Flush‑Eintrag fasst **tausende Operationen** zusammen, die zu einem einzigen Delta netto werden.
* Viele Aggregate werden pro DB‑Aufruf gebündelt → DB‑Aufrufe tendieren gegen **O(I / batch_size)** pro Intervall statt **O(N)**.
* Sie vermeiden Hot‑Row‑Contention und I/O pro Anfrage; der Hot Path ist **rein im Speicher, O(1)**, mit atomarem Gating.

---

## Kleines Zahlenbeispiel

* **1,000,000** Operationen treffen für Schlüssel *K* innerhalb einer Sekunde ein; netto ergibt sich **+200**.
* **Per‑Request‑Design:** ~1,000,000 dauerhafte Writes (plus Locks/Contention).
* **VSA:** alle 1,000,000 im Speicher schalten, dann **ein einziges +200** persistieren (oder eines pro Zeit‑Bucket).
* **Ergebnis:** gleicher dauerhafter Endzustand, **Größenordnungen weniger I/O**.

---

## Ihre Benchmarks (fairer Vergleich)

* **VSA‑Hot‑Path:** **16–20M Ops/s** bei nur **~0.8k–1.3k DB‑Aufrufen/s** über 128 Schlüssel (≈ **13k–28k Ops pro DB‑Aufruf**).
* **Baselines (Token/Leaky Bucket, I/O pro Anfrage):** **~25–27k Ops/s** und **~50–53k DB‑Aufrufe/s**.
  → Es wird weiterhin persistiert, aber **weit weniger, dafür reichere Datensätze**.

---

## Was ist das **koaleszierte Δ**?

Das **koaleszierte Δ (Delta)** ist die **Saldoänderung**, die **im Speicher** für einen Schlüssel seit dem letzten dauerhaften Commit akkumuliert wurde—das eine Update, das **viele** eingehende Operationen zusammenfasst.

* Konzeptionell:
  `new_persistent_state = old_persistent_state ⊕ Δ`
* Beispiel: Ereignisse auf Schlüssel *K*: `+1, +1, −1, +3, −2 → Δ = +2`.
  Jede Anfrage wird exakt im Speicher geschaltet; **flush Δ = +2 einmal**, nicht fünf Writes.

### Sicherheitsanforderungen (zur Wahrung der Exaktheit)

* **Atomares Gating** gegen **(dauerhafte Basis + Δ im Speicher)**.
* **Idempotente Flushes** (Replays führen nicht zur Doppel‑Anwendung).
* Verwendung **assoziativer/kommutativer** Aggregation, damit die Ankunftsreihenfolge egal ist.

---

## Korrektheit: „keine Über‑Freigabe‑Drift“

Für jede Zeit *t* überschreiten zugelassene Operationen **A(t)** niemals das erzeugte Budget **B(t)**:
**A(t) ≤ B(t)**.
Abstürze können vorübergehend zu **Unter‑Freigabe** (konservativ) führen, aber niemals zu Überbelegung.

---

## Wann der Vorteil entfällt

Wenn Sie **jedes einzelne Ereignis** dauerhaft speichern **müssen** (z. B. Audit/Event‑Sourcing ohne Koaleszierung), ist der Persistenzvorteil der VSA hinfällig—verwenden Sie stattdessen ein WAL/Event‑Log. VSA zielt auf **exakte Zulassung mit minimalem dauerhaftem I/O**, nicht auf die Archivierung jedes Events.

---

## Fazit

Alles, was **zählt**, wird weiterhin dauerhaft, aber Sie zahlen nicht mit Platte (oder Contention) für die Flut an Zwischenrauschen. Das ist der Gewinn: **O(I)** dauerhafte Arbeit mit **I ≪ N**, ein O(1)‑Hot‑Path und starke Korrektheitsgarantien.