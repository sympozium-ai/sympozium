# sympozium-crds

The Sympozium CRDs as a standalone Helm chart, so `helm upgrade` can roll schema changes forward (Helm 3 [never upgrades files under a chart's `crds/` directory](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/)).

Versioned in lockstep with the main `sympozium` chart — always upgrade this one first.

```sh
helm upgrade --install sympozium-crds ./charts/sympozium-crds \
  --namespace sympozium-system --create-namespace

helm upgrade --install sympozium ./charts/sympozium \
  --namespace sympozium-system \
  --skip-crds --set createNamespace=false
```

Uninstalling this chart deletes the CRDs and cascade-deletes every Sympozium custom resource in the cluster. Uninstall `sympozium` first.
