# cronlite

Runs commands on intervals from a plain text file.

cronlite is a tiny foreground scheduler for the jobs you would rather not
write a crontab or a systemd timer for: keep a list of commands in a text
file next to your project, run `cronlite run`, and watch their output scroll
by with timestamps and task names.

## Install

```sh
go install github.com/dan2756/cronlite@latest
```

## Usage

Create `cronlite.txt`:

```
# comments and blank lines are fine
every 5m                    ./scripts/sync.sh
every 1h30m                 rsync -a ~/notes/ backup:/notes/
at 09:00                    ./standup-reminder.sh
at 09:00,13:30 weekdays     say "stretch"
cron "*/15 9-17 * * 1-5"    ./check-queue.sh
every 10s  timeout=5s  name=ping   curl -fsS https://example.com/health
```

Then:

```sh
cronlite check   # validate the file
cronlite run     # run the scheduler until Ctrl-C
```

Each line is a schedule followed by a command. Commands run with `sh -c`
from the directory that contains the task file.

- `every <duration>` uses Go duration syntax (`10s`, `5m`, `1h30m`) plus `d`
  for days (`2d`).
- `at HH:MM[,HH:MM...]` runs daily at those local times. Add `weekdays`,
  `weekends` or a list of days such as `mon,wed,fri` (ranges like `mon-thu`
  work too) to limit it to certain days.
- `cron "<expr>"` takes a standard five-field cron expression: lists, ranges,
  steps and month/weekday names (`jan`, `mon`) all work.

To see when a cron expression fires:

```sh
$ cronlite next "*/15 9-17 * * 1-5" -n 3
Mon 2026-01-26 09:00 EST  in 13h9m
Mon 2026-01-26 09:15 EST  in 13h24m
Mon 2026-01-26 09:30 EST  in 13h39m
```

## Running

`cronlite run` stays in the foreground. Task output goes to stdout and
stderr, each line prefixed with a timestamp and the task name; cronlite's own
messages go to stderr and are marked with `--`:

```
2026-01-31 21:07:40 [ping] -- run 1 started
2026-01-31 21:07:40 [ping] {"status":"ok"}
2026-01-31 21:07:40 [ping] -- run 1 ok (112ms)
2026-01-31 21:07:50 [sync] -- skipped: still running
```

A task never overlaps itself: if it is still running when it is due again,
that run is skipped and logged. Runs missed while the machine was asleep are
dropped rather than fired in a burst.

| Flag           | Meaning                                                          |
|----------------|------------------------------------------------------------------|
| `-f FILE`      | task file to use                                                 |
| `--jitter DUR` | delay every run by a random amount up to `DUR` (`jitter=` wins)  |
| `--grace DUR`  | on shutdown, how long to wait for running tasks (default `10s`)  |

On SIGINT or SIGTERM cronlite stops scheduling, waits up to `--grace` for
running tasks to finish, then kills their process groups. A second Ctrl-C
kills them right away.

### Task options

Options go between the schedule and the command:

| Option            | Meaning                                                        |
|-------------------|----------------------------------------------------------------|
| `timeout=DUR`     | kill the run (its whole process group) after `DUR`             |
| `name=NAME`       | name shown in output; defaults to the program name (`sync`)    |
| `jitter=DUR`      | delay each run by a random amount between 0 and `DUR`          |
| `dir=PATH`        | working directory, relative to the task file; `~` is expanded  |
| `onfail=continue` | a failed run is reported but not treated as an error           |

Options are lowercase `key=value` words, so a command can still start with
an environment assignment: `every 1h LANG=C ./report.sh`.

Each run gets `CRONLITE_TASK` (the task name) and `CRONLITE_RUN` (1 for the
first run, 2 for the next, ...) in its environment.

cronlite reads `./cronlite.txt` if it exists, otherwise
`~/.config/cronlite/tasks`. Use `-f FILE` to pick another file.

## License

MIT
