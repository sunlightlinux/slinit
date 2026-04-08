#compdef slinitctl
# Zsh completion for slinitctl
# Place in a directory in your $fpath, or source directly.

_slinitctl_services() {
    local -a services
    local sock_args=()
    local i
    for ((i=2; i < CURRENT; i++)); do
        case "${words[i]}" in
            --system|-s) sock_args+=(--system) ;;
            --user|-u)   sock_args+=(--user) ;;
            --socket-path|-p)
                if [ $((i+1)) -lt $CURRENT ]; then
                    sock_args+=(--socket-path "${words[$((i+1))]}")
                fi
                ;;
        esac
    done
    services=( ${(f)"$(slinitctl ${sock_args[@]} list 2>/dev/null | sed 's/^\[.*\] //' | sed 's/ (.*//')"} )
    _describe 'service' services
}

_slinitctl() {
    local -a commands global_opts shutdown_types dep_types signals

    commands=(
        'list:List all loaded services'
        'ls:List all loaded services (alias)'
        'start:Start a service'
        'wake:Start without marking active'
        'stop:Stop a service'
        'release:Remove active mark'
        'restart:Restart a service'
        'status:Show service status'
        'is-started:Check if service is started'
        'is-failed:Check if service has failed'
        'shutdown:Initiate system shutdown'
        'trigger:Trigger a triggered service'
        'untrigger:Reset trigger state'
        'signal:Send signal to service process'
        'reload:Reload service config from disk'
        'unload:Unload stopped service from memory'
        'boot-time:Show boot timing analysis'
        'analyze:Show boot timing analysis (alias)'
        'catlog:Show buffered service output'
        'attach:Attach to service virtual TTY (Ctrl+] to detach)'
        'pause:Pause a service (SIGSTOP)'
        'continue:Continue a paused service (SIGCONT)'
        'once:Start service without auto-restart'
        'setenv:Set per-service environment variable'
        'unsetenv:Remove per-service environment variable'
        'getallenv:List service environment variables'
        'setenv-global:Set global environment variable'
        'unsetenv-global:Remove global environment variable'
        'getallenv-global:List global environment variables'
        'add-dep:Add runtime dependency'
        'rm-dep:Remove runtime dependency'
        'unpin:Remove start/stop pins'
        'enable:Enable service'
        'disable:Disable service'
        'query-name:Query canonical service name'
        'service-dirs:List service directories'
        'load-mech:Query service loader mechanism'
        'dependents:List service dependents'
        'platform:Detect and display virtualization/container platform'
    )

    global_opts=(
        '(-p --socket-path)'{-p,--socket-path}'[Control socket path]:path:_files'
        '(-s --system)'{-s,--system}'[Connect to system service manager]'
        '(-u --user)'{-u,--user}'[Connect to user service manager]'
        '--no-wait[Do not wait for command completion]'
        '--pin[Pin service in started/stopped state]'
        '(-f --force)'{-f,--force}'[Force stop even with dependents]'
        '--ignore-unstarted[Exit 0 if service already stopped]'
        '(-o --offline)'{-o,--offline}'[Offline mode for enable/disable]'
        '(-d --services-dir)'{-d,--services-dir}'[Service directory]:dir:_directories'
        '--from[Source service for enable/disable]:service:_slinitctl_services'
        '--use-passed-cfd[Use fd from SLINIT_CS_FD]'
        '(-q --quiet)'{-q,--quiet}'[Suppress informational output]'
        '(-h --help)'{-h,--help}'[Show help]'
        '--version[Show version]'
    )

    shutdown_types=(
        'halt:Halt the system'
        'poweroff:Power off the system'
        'reboot:Reboot the system'
        'kexec:Kexec into new kernel'
        'softreboot:Soft reboot (userspace only)'
    )

    dep_types=(
        'regular:Hard dependency'
        'waits-for:Soft dependency'
        'milestone:Milestone dependency'
        'soft:Soft dependency'
        'before:Ordering (before)'
        'after:Ordering (after)'
    )

    signals=(
        'SIGHUP' 'SIGINT' 'SIGQUIT' 'SIGKILL' 'SIGUSR1' 'SIGUSR2'
        'SIGTERM' 'SIGCONT' 'SIGSTOP' 'SIGTSTP'
        'HUP' 'INT' 'QUIT' 'KILL' 'USR1' 'USR2' 'TERM' 'CONT' 'STOP' 'TSTP'
    )

    _arguments -C \
        $global_opts \
        '1:command:->command' \
        '*::arg:->args'

    case $state in
        command)
            _describe 'command' commands
            ;;
        args)
            case ${words[1]} in
                start|stop|wake|release|restart|status|is-started|is-failed|\
                trigger|untrigger|reload|unload|unpin|enable|disable|\
                query-name|getallenv|catlog|attach|pause|continue|once|dependents)
                    _slinitctl_services
                    ;;
                shutdown)
                    _describe 'shutdown type' shutdown_types
                    ;;
                signal)
                    case $CURRENT in
                        2) _describe 'signal' signals ;;
                        3) _slinitctl_services ;;
                    esac
                    ;;
                setenv|unsetenv)
                    case $CURRENT in
                        2) _slinitctl_services ;;
                    esac
                    ;;
                add-dep|rm-dep)
                    case $CURRENT in
                        2) _slinitctl_services ;;
                        3) _describe 'dependency type' dep_types ;;
                        4) _slinitctl_services ;;
                    esac
                    ;;
            esac
            ;;
    esac
}

_slinitctl "$@"
