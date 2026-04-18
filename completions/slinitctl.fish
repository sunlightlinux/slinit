# Fish completion for slinitctl
# Place in ~/.config/fish/completions/ or /usr/share/fish/vendor_completions.d/

# Helper: list services from running daemon
function __slinitctl_services
    slinitctl --system list 2>/dev/null | string replace -r '^\[.*\] ' '' | string replace -r ' \(.*' ''
end

# Subcommands
set -l commands list ls start wake stop release restart status is-started is-failed is-newer-than is-older-than shutdown trigger untrigger signal pause continue cont once reload unload boot-time analyze catlog attach setenv unsetenv getallenv setenv-global unsetenv-global getallenv-global add-dep rm-dep unpin enable disable graph query-name service-dirs load-mech dependents list5 status5 platform completion

# Disable file completions by default
complete -c slinitctl -f

# Global flags
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -s p -l socket-path -rF -d 'Control socket path'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -s s -l system -d 'System service manager'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -s u -l user -d 'User service manager'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -l no-wait -d 'Do not wait for completion'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -l pin -d 'Pin service state'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -s f -l force -d 'Force stop'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -l ignore-unstarted -d 'Exit 0 if already stopped'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -s o -l offline -d 'Offline mode'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -s d -l services-dir -rF -d 'Service directory'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -l from -rfa '(__slinitctl_services)' -d 'Source service'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -l use-passed-cfd -d 'Use SLINIT_CS_FD'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -s q -l quiet -d 'Suppress output'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -s h -l help -d 'Show help'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -l version -d 'Show version'

# Subcommand completions
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a list -d 'List all services'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a ls -d 'List all services'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a start -d 'Start a service'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a wake -d 'Start without marking active'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a stop -d 'Stop a service'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a release -d 'Remove active mark'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a restart -d 'Restart a service'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a status -d 'Show service status'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a is-started -d 'Check if started'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a is-failed -d 'Check if failed'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a shutdown -d 'Initiate shutdown'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a trigger -d 'Trigger a service'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a untrigger -d 'Reset trigger'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a signal -d 'Send signal to service'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a reload -d 'Reload service config'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a unload -d 'Unload stopped service'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a boot-time -d 'Boot timing analysis'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a analyze -d 'Boot timing analysis'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a catlog -d 'Show service log buffer'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a attach -d 'Attach to service virtual TTY'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a pause -d 'Pause a service (SIGSTOP)'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a continue -d 'Continue a paused service'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a once -d 'Start without auto-restart'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a setenv -d 'Set service env var'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a unsetenv -d 'Remove service env var'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a getallenv -d 'List service env vars'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a setenv-global -d 'Set global env var'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a unsetenv-global -d 'Remove global env var'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a getallenv-global -d 'List global env vars'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a add-dep -d 'Add runtime dependency'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a rm-dep -d 'Remove runtime dependency'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a unpin -d 'Remove pins'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a enable -d 'Enable service'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a disable -d 'Disable service'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a query-name -d 'Query service name'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a service-dirs -d 'List service dirs'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a load-mech -d 'Query loader mechanism'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a dependents -d 'List dependents'
complete -c slinitctl -n "not __fish_seen_subcommand_from $commands" -a platform -d 'Detect platform'

# Service name completions for commands that take a service argument
for cmd in start wake stop release restart status is-started is-failed trigger untrigger reload unload unpin enable disable query-name getallenv catlog attach pause continue once dependents
    complete -c slinitctl -n "__fish_seen_subcommand_from $cmd" -a '(__slinitctl_services)' -d 'Service'
end

# shutdown type completions
complete -c slinitctl -n "__fish_seen_subcommand_from shutdown" -a 'halt poweroff reboot kexec softreboot' -d 'Shutdown type'

# signal completions
complete -c slinitctl -n "__fish_seen_subcommand_from signal" -a 'SIGHUP SIGINT SIGQUIT SIGKILL SIGUSR1 SIGUSR2 SIGTERM SIGCONT SIGSTOP SIGTSTP HUP INT QUIT KILL USR1 USR2 TERM CONT STOP TSTP' -d 'Signal'

# add-dep / rm-dep: dep type completions
complete -c slinitctl -n "__fish_seen_subcommand_from add-dep rm-dep" -a 'regular waits-for milestone soft before after' -d 'Dependency type'

# setenv/unsetenv: service name for first arg
for cmd in setenv unsetenv
    complete -c slinitctl -n "__fish_seen_subcommand_from $cmd" -a '(__slinitctl_services)' -d 'Service'
end
