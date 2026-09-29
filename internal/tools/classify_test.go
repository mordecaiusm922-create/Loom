package tools

import "testing"

// TestIacChangeTypeResistsSubstringEvasions is the permanent evasion suite
// for the argv classifier. Every command here slipped past the previous
// substring matcher (it only looked for "kubectl apply", "terraform
// destroy", ... with exactly one space) and reached DevMind's generic
// policy engine, which carries no blast-radius invariant.
func TestIacChangeTypeResistsSubstringEvasions(t *testing.T) {
	cases := map[string]string{
		"terraform -chdir=infra/prod apply -auto-approve":                      "terraform_apply",
		"terraform  destroy":                                                   "terraform_apply",
		"tofu -chdir=x destroy":                                                "terraform_apply",
		"& terraform apply":                                                    "terraform_apply",
		`& "C:\Program Files\Terraform\terraform.exe" apply`:                   "terraform_apply",
		"cd infra; terraform apply -auto-approve":                              "terraform_apply",
		"terraform plan -out=tfplan && terraform apply tfplan":                 "terraform_apply",
		"echo $(terraform destroy -auto-approve)":                              "terraform_apply",
		"TF_LOG=debug terraform apply":                                         "terraform_apply",
		"$env:TF_WORKSPACE='prod'; terraform apply":                            "terraform_apply",
		"sudo -E terraform apply":                                              "terraform_apply",
		"terragrunt run-all apply":                                             "terraform_apply",
		"terraform state rm aws_db_instance.main":                              "terraform_apply",
		"kubectl  delete pod x":                                                "k8s_manifest",
		"kubectl.exe delete pod x":                                             "k8s_manifest",
		"/usr/local/bin/kubectl delete ns checkout":                            "k8s_manifest",
		"kubectl --context prod-eks delete ns checkout":                        "k8s_manifest",
		"kubectl -n checkout delete deploy api":                                "k8s_manifest",
		"k delete pod x":                                                       "k8s_manifest",
		"oc delete project payments":                                           "k8s_manifest",
		"kubectl exec -it api-0 -- rm -rf /data":                               "k8s_manifest",
		"kubectl label node n1 role=db --overwrite":                            "k8s_manifest",
		"kubectl get pods -o name | xargs kubectl delete":                      "k8s_manifest",
		"helm --kube-context prod upgrade api ./chart":                         "helm_release",
		"helm -n payments uninstall api":                                       "helm_release",
		`bash -c "kubectl delete ns checkout"`:                                 "k8s_manifest",
		`sh -c 'terraform destroy -auto-approve'`:                              "terraform_apply",
		`pwsh -Command "terraform apply"`:                                      "terraform_apply",
		"pwsh -EncodedCommand dAB5AHAAZQA=":                                    "config_change",
		"iex (Get-Content cmd.txt -Raw)":                                       "config_change",
		"Invoke-Expression $cmd":                                               "config_change",
		"eval \"$DANGEROUS\"":                                                  "config_change",
		"aws --profile prod rds delete-db-instance --db-instance-identifier x": "config_change",
		"aws s3 rb s3://prod-backups --force":                                  "config_change",
		"aws iam attach-role-policy --role-name x --policy-arn y":              "iam_change",
		"vault kv put secret/db password=x":                                    "secret_rotation",
		"npx prisma migrate deploy":                                            "schema_migration",
		"bundle exec rails db:migrate":                                         "schema_migration",
		"ssh -i key.pem bastion kubectl delete ns checkout":                    "k8s_manifest",
		"Invoke-Command -ScriptBlock { terraform apply }":                      "terraform_apply",
		"python manage.py migrate":                                             "schema_migration",
	}
	for command, want := range cases {
		got, isChange := iacChangeType(command)
		if !isChange || got != want {
			t.Errorf("%q: change_type = %q isChange=%v, want %s", command, got, isChange, want)
		}
	}
}

// TestIacChangeTypeReadsStayUnclassified keeps the argv classifier from
// over-reaching: reads must stay on the low-risk generic policy engine.
func TestIacChangeTypeReadsStayUnclassified(t *testing.T) {
	reads := []string{
		"terraform validate",
		"terraform fmt -check",
		"terraform show -json tfplan",
		"terraform state list",
		"terraform output",
		"kubectl get pods -n product-catalog",
		"kubectl --context prod-eks get pods",
		"kubectl -n delete get pods", // a namespace literally named "delete"
		"kubectl rollout status deploy/api",
		"kubectl rollout history deploy/api",
		"kubectl logs api-0 --previous",
		"helm list -A",
		"helm -n delete status api",
		"aws iam list-users",
		"aws ec2 describe-instances --output json",
		"aws secretsmanager get-secret-value --secret-id x",
		"gcloud compute instances list --format=json",
		"az vm list --output table",
		"vault kv get secret/db",
		"echo terraform apply is dangerous",
		"grep -r 'kubectl delete' docs/",
	}
	for _, command := range reads {
		if got, isChange := iacChangeType(command); isChange {
			t.Errorf("%q: classified as %s, want unclassified", command, got)
		}
	}
}

func TestSplitCommandsHonorsQuotes(t *testing.T) {
	got := splitCommands(`echo "a; b" && kubectl delete pod 'x y'`)
	if len(got) != 2 {
		t.Fatalf("commands = %q, want 2", got)
	}
	if got[0][1] != "a; b" || got[1][3] != "x y" {
		t.Fatalf("commands = %q, quoted words must stay whole", got)
	}
}
