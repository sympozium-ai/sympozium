# Allowlist projection only. Never copy a spec, status, annotation, env or data map.
def identity: {kind, namespace:.metadata.namespace, name:.metadata.name,
  uid:.metadata.uid, resourceVersion:.metadata.resourceVersion};
def pod:
  {containers:[(.spec.initContainers[]?, .spec.containers[]?) |
    {name,image,mounts:[.volumeMounts[]? | {name,mountPath}]}],
   volumes:[.spec.volumes[]? | {name,secretRef:.secret.secretName,
     configMapRef:.configMap.name,pvcRef:.persistentVolumeClaim.claimName,
     hostPath:.hostPath.path}]};
{
 apiVersion:"sympozium.ai/celln-migration-inventory-v1",
 migrationAuthorized:false,
 proposedDeletions:[],
 resources:[.[] | .items[] | identity +
   (if .kind == "AgentRuntime" then
     {runtime:{revision:.spec.celln.revision,contractVersion:.spec.celln.contractVersion,
       executable:.spec.celln.executable.hash,closure:.spec.celln.closure.hash,
       mote:.spec.celln.mote.hash,publisherKey:.spec.celln.publisherKey,
       profileRef:.spec.cellnProfileRef}}
    elif .kind == "CellnTool" then
     {tool:{revision:.spec.revision,executable:.spec.executable.hash,
       closure:.spec.closure.hash,publisherKey:.spec.publisherKey,
       invocationABI:.spec.invocationABI,argumentsSchema:.spec.argumentsSchema.hash,
       resultSchema:.spec.resultSchema.hash}}
    elif .kind == "AgentRun" then
     {run:{backend:.spec.backend,lifecycle:.spec.executionLifecycle,phase:.status.phase,
       actionID:.status.cellnActionID,
       parent:(if .status.cellnParent then .status.cellnParent |
         {createAttempted,acceptedTurns,activeTurn,
          binding:{principal:.binding.principal,runUID:.binding.runUID,
            incarnation:.binding.incarnation,launchProfile:.binding.launchProfile,
            specSHA256:.binding.specSHA256},
          initialTurn:{id:.initialTurn.id,child:.initialTurn.child,
            attempted:.initialTurn.attempted}} else null end)}}
    elif .kind == "Pod" then pod
    elif .kind == "PersistentVolumeClaim" then
     {storage:{volumeName:.spec.volumeName,storageClassName:.spec.storageClassName,
       phase:.status.phase}}
    elif .kind == "ConfigMap" then {}
    else error("unsupported inventory kind") end)] | sort_by(.kind,.namespace,.name),
 unresolved:["no coherent cross-resource snapshot is asserted",
   "grant intersections and exact selected sources require independent operator verification",
   "host registrations, owner targets, credential file references and journals require owner-local inventory",
   "provider route compatibility and durable ledger continuity are not established",
   "no catalogue or policy proposal is authorized by this inventory"]
}
