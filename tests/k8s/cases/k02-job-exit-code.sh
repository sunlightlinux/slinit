# A batch workload under slinit. Kubernetes decides whether a Job
# succeeded from the container's exit code, so slinit has to hand it the
# workload's code rather than its own. A Job that fails must be Failed,
# with the real code visible in the pod's terminated state — that is
# what an operator and every CI pipeline reads.

trap 'k delete pod k02-ok k02-fail --now >/dev/null 2>&1' EXIT

for pair in ok:0 fail:7; do
    name=k02-${pair%%:*}
    code=${pair##*:}

    svc_configmap $name-svc \
"boot:::type = process
command = /bin/sh -c \"sleep 1; exit $code\"
restart = no
"

    cat <<EOF | k apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $name
spec:
  restartPolicy: Never
  containers:
  - name: slinit
    image: $IMAGE
    imagePullPolicy: Never
    command: ["/sbin/slinit", "-o"]
    volumeMounts:
    - name: svc
      mountPath: /etc/slinit.d
  volumes:
  - name: svc
    configMap:
      name: $name-svc
EOF

    want_phase=Succeeded
    [ "$code" != "0" ] && want_phase=Failed
    wait_pod_phase $name $want_phase 60
    check $? "workload exiting $code leaves the pod $want_phase"

    got=$(term_exit_code $name)
    [ "$got" = "$code" ]
    check $? "kubernetes sees exit code $code (got '$got')"
done

summary
