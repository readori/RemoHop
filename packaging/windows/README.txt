RemoHop on Windows
=================

Run `remohop host` in a terminal for a session you control, or install it as a
service from an ELEVATED prompt (Run as administrator):

    remohop.exe service install
    remohop.exe service status
    remohop.exe service logs -f
    remohop.exe service uninstall

The service runs as LocalSystem, so whoever could replace the program it starts
would get LocalSystem. `service install` therefore copies remohop.exe to
%ProgramFiles%\RemoHop (only administrators can change it) and the service runs
that copy. After installing a newer remohop.exe, run `remohop.exe service
install` again from an elevated prompt: it stops the service, replaces the copy
and starts it again. `service uninstall` removes the copy too.

The service is named "RemoHop", starts automatically (delayed) and is restarted
by the Service Control Manager when it fails. It runs as LocalSystem in
session 0, which is isolated from the interactive desktop: shell, file
transfer and port forwarding work; screenshots and remote desktop of the
console do not, unless the service is installed for unattended access:

    remohop.exe service install --unattended

Then the service shows and controls the console session, including the
sign-in screen, the lock screen and Ctrl+Alt+Del (the last needs the
"software Secure Attention Sequence" policy). Unattended access is powerful:
anyone who can log in to this host controls the machine as SYSTEM. Without
it, run `remohop host` inside the user's session (for example from a Task
Scheduler task "At log on").

State (identity key, pairings, host password) is in %ProgramData%\RemoHop,
owned by the Administrators and open only to SYSTEM and Administrators;
install refuses a directory there that another user created (it could hold
planted credentials). The service's output is in
%ProgramData%\RemoHop\service.log (rotated to service.log.1 at 10 MiB). Read
the ID and password of the service instance with:

    set REMOHOP_CONFIG_DIR=%ProgramData%\RemoHop
    remohop.exe status

Code signing: Windows binaries are not signed yet. Windows SmartScreen will
warn on first run until they are signed with an Authenticode certificate.
