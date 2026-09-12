# Plano — Agendamento de workflows com o modal de job do tajobs

## Objetivo
No **tabelhaglue**, habilitar qualquer workflow pelo TUI (`e`) usando o **mesmo modal de configuração de datas do tabelhajobs**, com suporte total a recorrência (one-shot, diário, semanal, mensal, ciclo custom, manual). O form e a definição de datas viram um **pacote compartilhado no tabelhatuiui**, e o **tajobs migra** pra ele (single source of truth). Horário escolhido persiste num **sidecar** `~/.config/taglue/schedules.toml`, sem reescrever os TOML do usuário.

## Decisões (fechadas)
- **D1**: tajobs migra na mesma leva.
- **D2**: **presets descartados**. O form de recorrência já é o atalho; sem campo extra de presets.
- **D3**: **cycle com paridade total** — glue ganha script wrapper com auto-reagendamento (`.recur` + reescrita de `OnCalendar=`), igual tajobs.
- **D4**: recorrência (diário/semanal/mensal/ciclo) é o coração do recurso; **`manual`** é só mais uma opção do form e no glue mapeia pra **no-op** (fecha o modal sem agendar).

---

## Fase A — tabelhatuiui: request + pacote compartilhado `schedule`

**A1. Request**: `~/codigo/tabelhadev/tabelhatuiui/requests/20260912-schedule-form.md` (copiar `_template.md`, prioridade high, autor: tabelhaglue).

**A2. Novo pacote `schedule/`** no tabelhatuiui (arquivos: `schedule/schedule.go`, `schedule/form.go`, `schedule/calendar.go`, `schedule/script.go`, testes):

Tipos e dados (portados do tajobs):
```go
type Kind int
const (
    KindOneshot Kind = iota
    KindDaily
    KindWeekly
    KindMonthly
    KindCycle
    KindManual
)

type Schedule struct {
    Kind       Kind
    Minute, Hour int
    DOM, Month   int          // oneshot/cycle: primeira execução (DD/MM)
    Weekdays   []time.Weekday // weekly
    DayOfMonth int            // monthly
    Cycle      []int          // cycle
}
```

API:
- `func Groups(s Schedule) []*huh.Group` — **a peça-chave**: o select de recurrência + grupos condicionais (date DD/MM + time, weekly weekdays + time, monthly dayOfMonth + time, cycle + date + time, time só pra daily), com `WithHideFunc` por kind (espelho de `forms.go:139-238`). Cada app monta seu próprio `huh.NewForm(seus grupos locais..., schedule.Groups(s)...)`.
- `func (s Schedule) OnCalendar(now time.Time) string` — dispatch:
  - daily → `*-*-* HH:MM:00`
  - weekly → `Mon,Tue *-*-* HH:MM:00`
  - monthly → `*-*-DD HH:MM:00`
  - oneshot/cycle → timestamp absoluto com rollover de ano (`computeOnCalendar`, jobs.go:394)
  - manual → `""`
- Validadores/parsers exportados: `ValidateDDMM`, `ValidateHHMM`, `ValidateDayOfMonth`, `ValidateCycle`, `ParseDDMM`, `ParseHHMM`, `ParseCycle`.
- Script tails (pra oneshot/cycle):
  - `OneshotCleanupTail(timerPath, servicePath string) string` (jobs.go:501)
  - `CycleRescheduleTail(recurPath, timerPath, timerName string) string` (jobs.go:514, via `strings.NewReplacer` — não `fmt.Sprintf`, bash tem `%` literal)
- `func (s Schedule) String() string` — resumo humano ("diário 21:00", "semanal Seg,Qui 09:00", "mensal dia 15 08:00", "one-shot 12/09 21:00", "ciclo 2 4 5", "manual") pro painel de metadados.
- Helpers de weekday: `WeekdayAbbr`, ordenação Mon..Sun.

**A3. Docs**: CHANGELOG entry + bump **v0.6.0**; README com a API do pacote.

---

## Fase B — tabelhaglue: modal, sidecar e scheduler

**B1. Dep**: `go.mod` → `tabelhatuiui v0.6.0` (+ `go.sum`).

**B2. `Workflow.Schedule` estendido** (`workflow.go`):
```go
type Schedule struct {
    OnCalendar string            `toml:"on_calendar"` // escape hatch raw (existente)
    // structured (de TOML ou sidecar):
    Kind       string            `toml:"kind"`        // oneshot|daily|weekly|monthly|cycle|manual
    Hour, Minute int             `toml:"hour"` `toml:"minute"`
    Weekdays   []string          `toml:"weekdays"`
    DayOfMonth int               `toml:"day_of_month"`
    DOM, Month int               `toml:"dom"` `toml:"month"`
    Cycle      []int             `toml:"cycle"`
}
```
- `loadWorkflow`: lê TOML; se structured vazio **e** sidecar tem entrada → preenche do sidecar. `on_calendar` raw vence sempre.
- `workflowEntry` ganha `sched schedule.Schedule` resolvido (pra TUI usar direto).

**B3. Sidecar** (`schedule.go`): `~/.config/taglue/schedules.toml`
```toml
[post-suggestions]
kind = "daily"
hour = 7
minute = 0
```
- `loadSchedules() (map[string]WorkflowSchedule)`, `saveSchedule(name string, s Schedule) error` (read-modify-write preservando entradas).
- Persistência só do que o TUI escreve; TOML do workflow nunca é reescrito (mantém comentários).

**B4. Scheduler** (`schedule.go`): refactor do `EnableWorkflow`
- Entrada: `sched schedule.Schedule` (structured) OU raw `on_calendar`.
- `daily/weekly/monthly`: `ExecStart=<bin> run <name>` (comportamento atual).
- `oneshot`: gera wrapper `~/.local/state/taglue/<name>.sh` = `taglue run <name>` + `OneshotCleanupTail`; `ExecStart=<script>`; schedule fica no sidecar (só o timer é removido ao rodar, `IsScheduled` deriva do `.timer` → metadata mostra inativo depois).
- `cycle`: wrapper + `<name>.recur` (`"2 4 5\n0"`) + `CycleRescheduleTail`; `ExecStart=<script>`.
- `manual`: não chama `EnableWorkflow` (no-op).
- `DisableWorkflow`: também remove wrapper + `.recur` se existirem.
- `validateCalendar` continua só pro raw.

**B5. TUI** (`tui.go`, padrão tajobs model.go:958-1005 + renderModal:1552):
- Model: `scheduleForm *huh.Form`, `scheduleEntry *workflowEntry`.
- `e`:
  - workflow **com** schedule efetivo → toggle direto (hoje).
  - **sem** schedule → `m.scheduleForm = huh.NewForm(schedule.Groups(zero Schedule)...)` (todas as kinds, incl. manual), retorna `m.scheduleForm.Init()`.
- Update: `esc` fecha modal (sem sidecar, sem timer); `State == huh.StateCompleted` → lê campos (`GetString("time")`, `Get("weekdays")`, etc. — espelho model.go:970-1005), monta `schedule.Schedule`:
  - `KindManual` → só fecha.
  - senão → `saveSchedule(entry.File, s)` + `EnableWorkflow`.
- View: modal `renderModal("agendar: <nome>", m.scheduleForm.View())` com `theme.Modal()`.
- Metadados: mostra `on_calendar` raw se houver, senão `sched.String()`.

**B6. Testes glue**: sidecar round-trip preservando entradas; merge (TOML vence / sidecar preenche gap); geração de wrapper pra oneshot/cycle; `manual` no-op.

---

## Fase C — tabelhajobs: migração pro pacote `schedule`

- **C1**: `go.mod` → `tabelhatuiui v0.6.0`.
- **C2**: remover local: `RecurrenceKind`+consts, `jobSchedule`, `computeOnCalendar`, `computeRecurringOnCalendar`, `systemdWeekdayAbbr`, `isoWeekday`, `validateDDMM/HHMM/DayOfMonth/Cycle`, `parseDDMM/HHMM/Cycle`, `oneshotCleanupTail`, `cycleRescheduleTail` → tudo via `schedule.*`.
- **C3** `forms.go`: `newCreateForm`/`newEditForm` = `huh.NewForm(grupos locais name/commands/notes..., schedule.Groups(s)...)`; montagem de resultado usa `ParseHHMM` etc. do shared.
- **C4** `jobs.go`/`model.go`: `jobSchedule` → `schedule.Schedule`; `createJob` usa `sched.OnCalendar(now)` + tails do shared; `rescheduleJob` idem.
- **C5**: build/vet/test verdes; CHANGELOG bump; README se necessário.

---

## Fase D — validação final
- `go build ./...`, `go vet ./...`, `go test ./...` nos **3 repos**.
- Glue: rodar os 5 workflows headless; round-trip `enable`/`disable`; agendar `post-suggestions` pelo TUI (modal) e conferir timer.

## Ordem de execução
A → B → C → D (A antes de B/C; B e C podem seguir juntos depois de A).

## Pontos de atenção
- **huh não concatena forms** → a API `Groups()` resolve (apps montam o próprio `huh.NewForm`).
- Nome `schedule` (pkg) × `Schedule` (tipo): usar alias/import nomeado pra evitar colisão em glue/tajobs.
- Wrapper script usa caminho absoluto do binário (`taglueBinPath` já existe, schedule.go:51).
- Sidecar TOML decodifica `kind` string→`schedule.Kind` (mapa próprio no glue).
- `esc` no modal = cancelar limpo (não grava sidecar).
