# Agent In Management = False (Default)

With agent-in-mgmt-cluster==false, the drift-detection-manager is installed on the managed cluster as seen below:
```sh
➜  ~ khmc get pod -A
NAMESPACE        NAME                                        READY   STATUS    RESTARTS   AGE
ingress-nginx    ingress-nginx-controller-86bd747cf9-8vrhv   1/1     Running   0          19m
kube-system      aws-cloud-controller-manager-nfc6p          1/1     Running   0          21m
kube-system      calico-kube-controllers-6cd7d8cc9f-rnmqw    1/1     Running   0          22m
kube-system      calico-node-fksvm                           1/1     Running   0          22m
kube-system      calico-node-j8j9z                           1/1     Running   0          21m
kube-system      coredns-679c655b6f-bzb5m                    1/1     Running   0          21m
kube-system      coredns-679c655b6f-vg5q5                    1/1     Running   0          21m
kube-system      ebs-csi-controller-977d5cc56-d2pmp          5/5     Running   0          22m
kube-system      ebs-csi-controller-977d5cc56-rwxmp          5/5     Running   0          22m
kube-system      ebs-csi-node-2klrx                          3/3     Running   0          22m
kube-system      ebs-csi-node-zc7n7                          3/3     Running   0          21m
kube-system      kube-proxy-gfh9h                            1/1     Running   0          21m
kube-system      kube-proxy-l7vcq                            1/1     Running   0          22m
kube-system      metrics-server-78c4ccbc7f-tnkhg             1/1     Running   0          22m
projectsveltos   drift-detection-manager-5d76f698fd-t2gf4    1/1     Running   0          18m
projectsveltos   sveltos-agent-manager-75bfd75685-c9pd7      1/1     Running   0          22m
```
Also the ResourceSummary CRD is only installed on the managed cluster and not on the management cluster.

# Agent In Management = True

There is no agent deployed on the managed cluster:
```sh
➜  ~ khmc get pod -A
NAMESPACE       NAME                                        READY   STATUS    RESTARTS   AGE
ingress-nginx   ingress-nginx-controller-86bd747cf9-rb2jk   1/1     Running   0          4m22s
kube-system     aws-cloud-controller-manager-v6th8          1/1     Running   0          5m8s
kube-system     calico-kube-controllers-6cd7d8cc9f-dbrlg    1/1     Running   0          6m36s
kube-system     calico-node-2kk5b                           1/1     Running   0          6m33s
kube-system     calico-node-frzjz                           1/1     Running   0          6m19s
kube-system     coredns-679c655b6f-2bh6s                    1/1     Running   0          6m15s
kube-system     coredns-679c655b6f-2g9ln                    1/1     Running   0          6m15s
kube-system     ebs-csi-controller-977d5cc56-8b24v          5/5     Running   0          6m36s
kube-system     ebs-csi-controller-977d5cc56-gk8gj          5/5     Running   0          6m36s
kube-system     ebs-csi-node-464xt                          3/3     Running   0          6m19s
kube-system     ebs-csi-node-xp6qz                          3/3     Running   0          6m33s
kube-system     kube-proxy-blffc                            1/1     Running   0          6m24s
kube-system     kube-proxy-p7zhr                            1/1     Running   0          6m19s
kube-system     metrics-server-78c4ccbc7f-zffdl             1/1     Running   0          6m32s
```

Also the deployments in the projectsveltos namespace on the management cluster look like this. For some reason 2 sveltos-agents have been created:
```sh
➜  ~ ksveltos get deployments
NAME                                 READY   UP-TO-DATE   AVAILABLE   AGE
access-manager                       1/1     1            1           6m21s
addon-controller                     1/1     1            1           6m21s
classifier-manager                   1/1     1            1           6m21s
conversion-webhook                   1/1     1            1           6m21s
event-manager                        1/1     1            1           6m21s
hc-manager                           1/1     1            1           6m21s
sc-manager                           1/1     1            1           6m21s
shard-controller                     1/1     1            1           6m21s
sveltos-agent-i4xa4x9ipb997vif5vw9   1/1     1            1           2m28s
sveltos-agent-xful3msys9yt9get4rnq   1/1     1            1           5m28s
```

There is a sveltoscluster created in the `mgmt` namespace:
```sh
➜  ~ kubectl get sveltosclusters.lib.projectsveltos.io -A          
NAMESPACE   NAME   READY   VERSION
mgmt        mgmt   true    v1.30.0
```

Now after enabling `SyncMode=ContinuousWithDriftDetection` on the ClusterDeployment, we can see that the drift detection manager has been started on the management cluster:
```sh
➜  ~ ksveltos get deployments.apps 
NAME                                   READY   UP-TO-DATE   AVAILABLE   AGE
access-manager                         1/1     1            1           15m
addon-controller                       1/1     1            1           15m
classifier-manager                     1/1     1            1           15m
conversion-webhook                     1/1     1            1           15m
drift-detection-sxxt62u5qbwycsdgw19g   1/1     1            1           116s
event-manager                          1/1     1            1           15m
hc-manager                             1/1     1            1           15m
sc-manager                             1/1     1            1           15m
shard-controller                       1/1     1            1           15m
sveltos-agent-i4xa4x9ipb997vif5vw9     1/1     1            1           11m
sveltos-agent-xful3msys9yt9get4rnq     1/1     1            1           14m
```

# Findings

So as we can see even in agent in management mode, the `ResourceSummary` object is still created in the managed cluster.
My idea was to watch for `ResourceSummary.status.helmResourcesChanged` field to check if a drift had occurred.
But that does not seem feasible to do from the management cluster.

So one alternative could be that the `ClusterSummary.status.featureSummaries[].hash` field is set to `nil` (WAHAB: 2) 
when the addon-controller detects `ResourceSummary.status.helmResourcesChanged=true`.

Since the `ClusterSummary` field is in the management cluster, we can assume that drift has occurred if 
`ClusterSummary.status.featureSummaries[].hash=nil` provided that this field is not set to nil in any other case.

// WAHAB (drift): If you want to rely on hash = nil to check if drift has occurred
// then it has to match with isReady. So basically if isReady=false then
// hash being nil does not mean that a drift had occurred.
// Actually, since FeatureStatusFailed is used, the only way to be sure
// that hash = nil is drift detection might be to check for the status?
// The reason I think this is that if you search with `FeatureSummaries[i].Hash`
// in the entire repo, you will only find one location where hash is set to nil
// and status is set to FeatureStatusProvisioning, and this is the place
// where the drift has been detected.
// 
// Additionally we could keep a previous hash value in memory in HMC so that:
//
// if hash = nil:
//   if isReady = false:
//     - since cluster is not ready can't be sure if there is drift so ignore
//   else:
//     if prevHash = nil:
//       - this means that it is the 1st time that resource getting provisioned
//       - or that the HMC controller get got (re)started so it is observing for the 1st time
//       - so ignore
//     else:
//       - drift occurred so notify