import os
import yaml
import subprocess

kubectl = os.environ['KUBECTL'] if os.environ['KUBECTL'] != "" else "kubectl"
namespace = os.environ['NAMESPACE'] if os.environ['NAMESPACE'] != "" else "hmc-system"
template_dir = "templates/hmc-templates/files/templates"

def get_namespace(templ) -> str:
  x = yaml.safe_load(templ)
  print(x)
  print("--------------")

for x in os.listdir(template_dir):
  filepath = os.path.join(template_dir, x)
  with open(filepath, "r") as f:
    get_namespace(f.read())
  args = "{kubectl} -n {namespace} apply -f {filepath}".format(kubectl=kubectl, namespace=namespace, filepath=filepath).split()
  # print(args)
