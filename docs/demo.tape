# vhs tape for the README demo — render with: vhs docs/demo.tape
# Assumes a cluster with the shipmate controller and the demo `hello` App
# (see hack/demo-microk8s.sh) reachable via ~/.kube/shipmate-demo.config.
Output docs/demo.gif
Set Shell "bash"
Set FontSize 15
Set Width 1000
Set Height 620
Set TypingSpeed 40ms
Set Theme "Catppuccin Mocha"

Hide
Type "export KUBECONFIG=$HOME/.kube/shipmate-demo.config PS1='$ ' && clear"
Enter
Show

Type "# one CR = a whole app: deployment, service, ingress, backups"
Enter
Type "kubectl get apps"
Enter
Sleep 3s

Type "curl -sI http://hello.62.238.30.198.nip.io | head -n1"
Enter
Sleep 3s

Type "# break it on purpose — shipmate heals it"
Enter
Type "kubectl delete deployment hello && kubectl get deployment hello"
Enter
Sleep 4s

Type "# scale by editing the CR, not the deployment"
Enter
Type `kubectl patch app hello --type merge -p '{"spec":{"replicas":4}}'`
Enter
Sleep 4s
Type "kubectl get app hello"
Enter
Sleep 3s

Type "# delete the CR — everything it owns is garbage-collected"
Enter
Type "kubectl delete app hello"
Enter
Sleep 3s
Type "kubectl get deploy,svc,ingress"
Enter
Sleep 4s
