# vhs tape for the database + TLS proof — render with:
#   vhs docs/demo-database-tls.tape
# Assumes hack/demo-database-tls.sh already ran successfully against the
# cluster at ~/.kube/shipmate-demo.config (cert-manager installed, the
# db-demo App provisioned with a real Let's Encrypt certificate).
Output docs/demo-database-tls.gif
Set Shell "bash"
Set FontSize 15
Set Width 1000
Set Height 700
Set TypingSpeed 40ms
Set Theme "Catppuccin Mocha"

Hide
Type "export KUBECONFIG=$HOME/.kube/shipmate-demo.config PS1='$ ' && clear"
Enter
Show

Type "# one App, spec.database + spec.tls set — no compose file, no cert-bot cron"
Enter
Type "kubectl get app db-demo"
Enter
Sleep 3s

Type "# a real, publicly-trusted cert — no -k needed"
Enter
Type "curl -sv https://db-demo.178.105.147.131.nip.io/ 2>&1 | grep -E 'issuer:|SSL certificate verify|HTTP/2 200'"
Enter
Sleep 4s

Type "# write a row directly into the operator-provisioned postgres"
Enter
Type `kubectl exec db-demo-db-0 -- psql -U db-demo -d db-demo -c "CREATE TABLE proof(n int); INSERT INTO proof VALUES (42);"`
Enter
Sleep 3s

Type "# kill the pod — simulates a crash or node reschedule"
Enter
Type "kubectl delete pod db-demo-db-0 && kubectl wait --for=condition=Ready --timeout=90s pod/db-demo-db-0"
Enter
Sleep 5s

Type "# same PVC, same generated credentials, same data — nothing regenerated"
Enter
Type `kubectl exec db-demo-db-0 -- psql -U db-demo -d db-demo -c "SELECT * FROM proof;"`
Enter
Sleep 4s
