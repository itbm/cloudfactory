# Screenshots of the web terminal

These screenshots show the web terminal on the tab **Commands** of a website (FR-802). They are
from the demonstration mode with `CF_DEMO_TERMINAL=true`, the terminal relay
(`cf-terminal-relay`) and the sample data of `api/database/seeders/DemoSeeder.php`. A test
program played the agent: it connected a local shell to the relay in a sample Laravel
directory, and it sent the commands and the recording to the panel at the end.

## 01-commands-tab

The tab **Commands** has three parts: **Terminal**, **Run a command** and the **Command log**.
Below them, **Terminal sessions** shows each session of the website. The commands of the sample
session show "In the terminal" in the command log.

![01-commands-tab](01-commands-tab.png)

## 02-terminal

The owner opens the terminal and types three commands. The shell is in the directory `/app` of
the website.

![02-terminal](02-terminal.png)

## 03-terminal-ended

The owner types `exit`. The panel shows the reason and the exit code. **Open a new terminal**
opens a new session.

![03-terminal-ended](03-terminal-ended.png)

## 04-command-log

The command log shows each command of the session, with the person, the time and the IP
address.

![04-command-log](04-command-log.png)

## 05-terminal-session

**Show session** shows the session: the state, the person, the time, the commands and the
recording of the output. **Download the recording** gives the recording as an asciicast file.

![05-terminal-session](05-terminal-session.png)

## 06-terminal-sessions

**Terminal sessions** shows each session, its state, the number of commands and how long it was
open.

![06-terminal-sessions](06-terminal-sessions.png)

## 07-terminal-phone

The terminal on a phone.

![07-terminal-phone](07-terminal-phone.png)
