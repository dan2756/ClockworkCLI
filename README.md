# cronlite

Runs commands on intervals from a plain text file.

cronlite is a tiny foreground scheduler for the jobs you would rather not
write a crontab or a systemd timer for: keep a list of commands in a text
file next to your project, run `cronlite run`, and watch their output scroll
by with timestamps and task names. No daemon, no database, no dependencies.

## Install

```sh
go install github.com/dan2756/cronlite@latest
```

## Quick start

Create `cronlite.txt`:

```
# comments and blank lines are fine
every 5m            ./scripts/sync.sh
every 1h30m         rsync -a ~/notes/ backup:/notes/
every 10s  timeout=5s  name=ping   curl -fsS https://example.com/health
at 09:00            ./standup-reminder.sh
at 09:00,13:30 weekdays  say "stretch"
cron "*/15 9-17 * * 1-5"  ./check-queue.sh
```

Then:

```sh
cronlite check   # validate the file
cronlite list    # see what runs when
cronlite run     # run the scheduler until Ctrl-C
```

## Commands

| Command                     | What it does                                                    |
|-----------------------------|-----------------------------------------------------------------|
| `cronlite run [-f FILE]`    | run the scheduler in the foreground                             |
| `cronlite list [-f FILE]`   | show each task with its schedule and next run time              |
| `cronlite check [-f FILE]`  | validate the task file; print every error with its line number  |
| `cronlite once [-f FILE]`   | run every task once, one after another, right now               |
| `cronlite next EXPR [-n N]` | print the next `N` (default 5) fire times of a schedule         |

`cronlite -h` prints a summary and `cronlite --version` the version.

### run

```
$ cronlite run
2026-02-01 22:45:44 [cronlite] -- loaded 6 tasks from cronlite.txt; watching it for changes
2026-02-01 22:45:54 [ping] -- run 1 started
2026-02-01 22:45:54 [ping] {"status":"ok"}
2026-02-01 22:45:54 [ping] -- run 1 ok (112ms)
2026-02-01 22:50:44 [sync] -- run 1 started
2026-02-01 22:50:44 [sync] pulled 3 files
2026-02-01 22:55:44 [sync] -- skipped: still running
```

Task output goes to stdout and stderr as the task wrote it, each line
prefixed with a timestamp and the task name. cronlite's own messages go to
stderr and are marked with `--`.

A task never overlaps itself: if it is still running when it is due again,
that run is skipped and logged. Runs missed while the machine was asleep are
dropped rather than fired in a burst.

cronlite checks the task file every couple of seconds and reloads it when it
changes, so you can edit tasks without restarting. Tasks whose schedule did
not change keep their place (an `every 1h` task is not reset by an unrelated
edit), and a run in progress is left to finish. If the edited file has
errors, they are logged with line numbers and the previous schedule stays in
effect until the file is fixed:

```
2026-05-03 19:26:05 [cronlite] -- cronlite.txt:4: at: invalid time "9:60" (want HH:MM between 00:00 and 23:59)
2026-05-03 19:26:05 [cronlite] -- cronlite.txt not reloaded; keeping the previous schedule
2026-05-03 19:26:21 [cronlite] -- reloaded cronlite.txt: 7 tasks (1 added)
```

| Flag           | Meaning                                                          |
|----------------|------------------------------------------------------------------|
| `-f FILE`      | task file to use                                                 |
| `--jitter DUR` | delay every run by a random amount up to `DUR` (`jitter=` wins)  |
| `--grace DUR`  | on shutdown, how long to wait for running tasks (default `10s`)  |

On SIGINT, SIGTERM or SIGHUP (the terminal closing; not under `nohup`)
cronlite stops scheduling, waits up to `--grace` for running tasks to
finish, then kills their process groups. A second Ctrl-C kills them right
away. A closed output pipe does not stop cronlite, so Ctrl-C on
`cronlite run | tee log`, which ends `tee` at once, still shuts the tasks
down this way.

### list

```
$ cronlite list
NAME              SCHEDULE                  NEXT RUN                            COMMAND
sync              every 5m                  in 5m*     Sun 2026-02-01 22:50:44  ./scripts/sync.sh
rsync             every 1h30m               in 1h30m*  Mon 2026-02-02 00:15:44  rsync -a ~/notes/ backup:/notes/
ping              every 10s                 in 10s*    Sun 2026-02-01 22:45:54  curl -fsS https://example.com/health
standup-reminder  at 09:00                  in 10h14m  Mon 2026-02-02 09:00     ./standup-reminder.sh
say               at 09:00,13:30 weekdays   in 10h14m  Mon 2026-02-02 09:00     say "stretch"
check-queue       cron "*/15 9-17 * * 1-5"  in 10h14m  Mon 2026-02-02 09:00     ./check-queue.sh

* every schedules count from when cronlite run starts
```

### check

```
$ cronlite check
cronlite.txt:3: every: invalid duration "5mins" (use e.g. 30s, 5m, 1h30m, 2d)
cronlite.txt:7: cron: hour: 24 out of range 0-23
```

### once

Runs every task immediately, in file order, and exits non-zero if any of
them failed. Handy for trying out a new task file or for CI. Each run sees
`CRONLITE_RUN=1`; jitter is ignored.

### next

Prints upcoming fire times, for checking an expression before it goes in
the task file:

```
$ cronlite next "*/15 9-17 * * 1-5" -n 3
Mon 2026-02-02 09:00 EST  in 10h14m
Mon 2026-02-02 09:15 EST  in 10h29m
Mon 2026-02-02 09:30 EST  in 10h44m
```

`EXPR` is a bare cron expression or any schedule as written in a task file
(`"at 09:00,13:30 weekdays"`, `"every 1h30m"`, `"cron @weekly"`). With
`--from` it starts at another time instead of now, which is handy for seeing
what happens around a daylight saving change:

```
$ TZ=America/New_York cronlite next "30 2 * * *" --from 2026-03-07 -n 2
Sat 2026-03-07 02:30 EST  in 2h30m
Sun 2026-03-08 03:00 EDT  in 1d2h
```

## Task file

cronlite reads `./cronlite.txt` if it exists, otherwise
`~/.config/cronlite/tasks` (`$XDG_CONFIG_HOME/cronlite/tasks` if that is
set). Use `-f FILE` to pick another file.

Each line is a schedule, optional options, and a command. Blank lines and
lines starting with `#` are ignored. Commands run with `sh -c`, from the
directory that contains the task file unless `dir=` says otherwise, so
pipes, redirects, `&&` and quoting all work as in a shell.

### Schedules

| Schedule                     | Examples                                                       |
|------------------------------|----------------------------------------------------------------|
| `every <duration>`           | `every 90s`, `every 1h30m`, `every 2d`                         |
| `at HH:MM[,HH:MM...] [days]` | `at 09:00`, `at 09:00,13:30 weekdays`, `at 18:00 mon,wed,fri`  |
| `cron "<5-field expr>"`      | `cron "*/15 9-17 * * 1-5"`, `cron "0 3 1 * *"`, `cron @daily`  |

- **every** uses Go duration syntax (`10s`, `5m`, `1h30m`, `250ms`) plus
  `d` for days. The first run comes one interval after `cronlite run`
  starts.
- **at** runs at those local wall clock times. Days can be `weekdays`,
  `weekends`, `daily`, or a list of day names and ranges such as
  `mon,wed,fri` or `mon-thu,sat`. Without days it runs every day.
- **cron** is standard cron syntax: `minute hour day-of-month month
  day-of-week`, with `*`, lists (`1,15`), ranges (`9-17`), steps (`*/15`,
  `9-17/2`) and names (`jan`-`dec`, `sun`-`sat`; `0` and `7` are both
  Sunday). As in classic cron, if both the day-of-month and day-of-week
  fields are restricted, a day matching either one fires. Dates that can
  never happen, such as `30 feb`, are rejected. The nicknames `@hourly`,
  `@daily` (`@midnight`), `@weekly`, `@monthly` and `@yearly`
  (`@annually`) work too, quoted or not: `cron @daily ./rotate-logs.sh`.

`at` and `cron` follow the local wall clock, including across daylight
saving changes. When clocks spring forward, a time that does not exist that
day (02:30, say) runs at the moment of the jump instead of being skipped.
When clocks fall back, a time that happens twice runs only the first time.
`every` is unaffected: it counts real elapsed time.

### Options

Options go between the schedule and the command:

| Option            | Meaning                                                        |
|-------------------|----------------------------------------------------------------|
| `timeout=DUR`     | kill the run (its whole process group) after `DUR`             |
| `name=NAME`       | name shown in output; defaults to the program name (`sync`)    |
| `jitter=DUR`      | delay each run by a random amount between 0 and `DUR`          |
| `dir=PATH`        | working directory, relative to the task file; `~` is expanded  |
| `onfail=continue` | a failed run is logged but not an error: `once` still exits 0  |

Options are lowercase `key=value` words, so a command can still start with
an environment assignment: `every 1h LANG=C ./report.sh`. Task names must
be unique; unnamed tasks get the program name, with `-2`, `-3` added when
several share one.

On timeout cronlite sends SIGTERM to the task's process group, and SIGKILL
two seconds later if anything is left.

A run ends when its shell exits. If the command starts something in the
background (`./server &`), cronlite does not wait for it, but keeps showing
its output under the task's name. Once the shell has exited, such a process
is on its own: neither `timeout=` nor stopping cronlite will stop it.

### Environment

Each run gets these on top of cronlite's own environment:

| Variable        | Value                                                |
|-----------------|------------------------------------------------------|
| `CRONLITE_TASK` | the task name                                        |
| `CRONLITE_RUN`  | the run number: 1 for the first run, then 2, 3, ...  |

## Exit codes

| Code | Meaning                                                                                    |
|------|--------------------------------------------------------------------------------------------|
| 0    | success; `run` exits 0 after a clean shutdown                                              |
| 1    | the task file is invalid or missing, (`once`) a task failed, or (`next`) `EXPR` is invalid |
| 2    | bad command line                                                                           |
| 130  | `once` was interrupted                                                                     |

## Platform notes

cronlite runs on Linux and macOS. Commands run with `/bin/sh`, each in its
own process group, so a timeout, or a shutdown while the command is still
running, also stops anything it started in the background, and Ctrl-C in
the terminal reaches cronlite rather than the tasks directly. Background
processes that outlive their command's shell are left alone (see
[Options](#options)).

## License

MIT
