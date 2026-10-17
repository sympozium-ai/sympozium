# AWS MCP with IAM Roles Anywhere

This guide connects a Sympozium Agent to the AWS MCP Server using temporary
AWS credentials from IAM Roles Anywhere. It uses an X.509 certificate and the
AWS `aws_signing_helper` credential process, so no long-lived AWS access key
or secret key is stored in Kubernetes.

The certificate's private key remains sensitive. Store it in a Kubernetes
Secret and mount it read-only into the MCP container. Do not put it in
`MCPServer.spec.deployment.secretRefs`: Sympozium injects those Secret values
as environment variables.

## How authentication works

```text
Agent → mcp-bridge → AWS MCP proxy → AWS SDK credential process
                                    → aws_signing_helper + X.509 certificate
                                    → IAM Roles Anywhere → temporary STS credentials
                                    → SigV4-signed AWS MCP requests
```

The proxy and credential helper run in the MCP server container. The AWS SDK
uses the configured `credential_process` when it needs credentials and runs it
again to refresh credentials as they expire. Credentials stay in the container
and are used to sign requests; they are not passed to the Agent or model.
The IAM role's permissions policy is one authorization control for the AWS
actions the session can perform. Effective permissions also depend on
applicable session policies, permissions boundaries, service control policies
(SCPs), and explicit denies. MCP tool filters and `toolsPrefix` only affect
which tools are registered with the Agent; they do not enforce AWS
authorization.

## Prerequisites

- A Sympozium cluster with the `MCPServer` CRD.
- An AWS account and permission to create IAM Roles Anywhere resources, an IAM
  role, and a least-privilege permissions policy.
- A certificate authority (CA) and an end-entity certificate issued by that
  CA for this workload. The CA private key must remain outside the cluster.
- A container registry reachable by the cluster.
- A Linux container image for your node architecture containing the AWS MCP
  proxy, `aws_signing_helper`, and a stdio entrypoint. The
  [reference implementation](https://github.com/Radkowski/sympozium-aws-mcp-roles-anywhere)
  includes a Dockerfile, entrypoint, and matching Sympozium manifest. Review
  and build it in your own supply chain; pin the proxy version and verify the
  helper checksum rather than using a floating `latest` image.
- The AWS MCP Server endpoint in a supported Region. See the [AWS MCP Server
  endpoints](https://docs.aws.amazon.com/general/latest/gr/aws-mcp.html).

The example below assumes an endpoint of the form
`https://aws-mcp.<region>.api.aws/mcp`. Keep the Roles Anywhere resources,
credential-helper region, and endpoint signing region aligned as required by
your deployment.

## 1. Issue the workload certificate

Use your organization's CA to issue a client certificate for the MCP workload.
The certificate must chain to the CA certificate you register with IAM Roles
Anywhere. Give it a workload-unique Subject Alternative Name (SAN) URI, such as
`spiffe://example.com/sympozium/aws-mcp`, and make sure the CA does not issue
that identity to another workload. The profile's attribute mapping must include
`x509SAN/URI` for the trust policy condition to work; AWS maps SAN URI values by
default. The
[reference implementation's provisioning notebook](https://github.com/Radkowski/sympozium-aws-mcp-roles-anywhere/blob/main/provisioning/iam-roles-anywhere-lab.ipynb)
demonstrates a disposable EasyRSA CA and client certificate for a lab. Do not
use its local CA for production; keep the CA private key outside the cluster.

## 2. Create the trust anchor, role, and profile

IAM Roles Anywhere exchanges a certificate chain trusted by a configured trust
anchor for temporary role credentials. Create a role whose trust policy is
restricted to this anchor and the workload certificate subject, then attach a
permissions policy for the AWS actions the Agent needs. This lab example
attaches the AWS managed `ViewOnlyAccess` policy; it is broad, so replace it
with a narrower custom policy for production.

In the AWS console, create an IAM Roles Anywhere trust anchor with your CA
certificate as its certificate bundle and enable it. Use the resulting trust
anchor ARN in the trust policy below. Create the trust anchor in the Region
where you will create the profile and request credentials.

The role trust policy must trust the IAM Roles Anywhere service principal and
should scope assumptions to the expected trust anchor and certificate
identity. For example, adapt the condition values to your CA and the unique
SAN URI in your certificate:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": { "Service": "rolesanywhere.amazonaws.com" },
    "Action": ["sts:AssumeRole", "sts:SetSourceIdentity", "sts:TagSession"],
    "Condition": {
      "ArnEquals": {
        "aws:SourceArn": "TRUST_ANCHOR_ARN"
      },
      "StringEquals": {
        "aws:PrincipalTag/x509SAN/URI": "spiffe://example.com/sympozium/aws-mcp"
      }
    }
  }]
}
```

Create the role and attach the lab policy (replace the placeholders):

```bash
aws iam create-role \
  --role-name sympozium-aws-mcp \
  --assume-role-policy-document file://trust-policy.json

aws iam attach-role-policy \
  --role-name sympozium-aws-mcp \
  --policy-arn arn:aws:iam::aws:policy/job-function/ViewOnlyAccess
```

Create a Roles Anywhere profile that permits this role, then record both ARNs.
The credential helper uses the trust anchor, profile, and role ARNs to request
temporary credentials:

```bash
aws rolesanywhere create-profile \
  --name sympozium-aws-mcp \
  --role-arns arn:aws:iam::123456789012:role/sympozium-aws-mcp \
  --enabled \
  --region us-east-1
```

For details, see the AWS [Roles Anywhere trust model](https://docs.aws.amazon.com/rolesanywhere/latest/userguide/trust-model.html),
[credential helper](https://docs.aws.amazon.com/rolesanywhere/latest/userguide/credential-helper.html),
and [create-profile command](https://docs.aws.amazon.com/cli/latest/reference/rolesanywhere/create-profile.html).

## 3. Build and publish the MCP container

Build an image for the architecture of the cluster nodes. Include a pinned
version of the AWS MCP proxy and `aws_signing_helper`. The entrypoint should
run the proxy in stdio mode, keeping stdout reserved for MCP messages and
writing diagnostics to stderr. The reference implementation linked above
shows one way to package these components; Sympozium does not publish or
maintain that image.

Push the image to a registry available to your cluster and use an immutable
version tag in the `MCPServer` below. Configure an image pull Secret on the
MCP server ServiceAccount if the registry is private.

## 4. Create the certificate Secret

Create a Secret containing the end-entity certificate and its private key.
The key is sensitive material: do not commit it, print it in command output,
or put it in a ConfigMap or environment variable. Use your certificate
issuer's filenames or rename them to match this example:

```bash
kubectl create secret generic aws-mcp-certificate \
  --namespace sympozium-system \
  --from-file=tls.crt=./work/client.crt \
  --from-file=tls.key=./work/client.key
```

Kubernetes mounts Secret volumes read-only in the pod specification below.
Enable encryption at rest for Kubernetes Secrets or use an external secret
provider, and tightly restrict RBAC access to the Secret in
`sympozium-system`. The example sets the projected files to mode `0400`; ensure
the helper process runs as a UID that can read them. Rotate the certificate
before it expires. Replacing the certificate Secret does not reissue the
certificate; follow your CA's issuance and rotation process.

## 5. Configure the AWS profile and MCPServer

Create a ConfigMap with an AWS shared config profile. The credential helper
uses the mounted certificate and key to obtain temporary credentials from IAM
Roles Anywhere. Replace the ARN placeholders and region with your values:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: aws-mcp-config
  namespace: sympozium-system
data:
  config: |
    [profile sympozium-mcp]
    region = us-east-1
    credential_process = /usr/local/bin/aws_signing_helper credential-process --certificate /certs/tls.crt --private-key /certs/tls.key --trust-anchor-arn arn:aws:rolesanywhere:us-east-1:123456789012:trust-anchor/TA_ID --profile-arn arn:aws:rolesanywhere:us-east-1:123456789012:profile/PROFILE_ID --role-arn arn:aws:iam::123456789012:role/sympozium-aws-mcp
```

Apply the ConfigMap, then create the `MCPServer`. Set `image` to the image you
built and pushed. Its entrypoint path and environment variables must match
that image's proxy configuration.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: aws-mcp
  namespace: sympozium-system
spec:
  transportType: stdio
  toolsPrefix: aws
  timeout: 180
  deployment:
    image: registry.example.com/aws-mcp-roles-anywhere:1.0.0
    cmd: /app/entrypoint.sh
    env:
      AWS_CONFIG_FILE: /config/config
      AWS_PROFILE: sympozium-mcp
      AWS_REGION: us-east-1
      MCP_ENDPOINT: https://aws-mcp.us-east-1.api.aws/mcp
    volumes:
      - name: aws-config
        configMap:
          name: aws-mcp-config
      - name: aws-certificate
        secret:
          secretName: aws-mcp-certificate
          defaultMode: 0400
    volumeMounts:
      - name: aws-config
        mountPath: /config
        readOnly: true
      - name: aws-certificate
        mountPath: /certs
        readOnly: true
    resources:
      requests:
        cpu: 100m
        memory: 256Mi
      limits:
        memory: 512Mi
```

Apply both resources and wait for the `MCPServer` deployment to become ready:

```bash
kubectl apply -f aws-mcp-config.yaml
kubectl apply -f aws-mcp-server.yaml
kubectl get mcpserver aws-mcp -n sympozium-system
```

The proxy image must not contain static AWS access keys. Do not add
`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, or `AWS_SESSION_TOKEN` to the
deployment; configure only the profile and certificate-based credential
process.

## 6. Attach the MCP server to an Agent

Reference the `MCPServer` by name in the Agent. The prefix namespaces the MCP
tools to avoid name collisions; it does not grant IAM permissions.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: aws-inventory
  namespace: default
spec:
  agents:
    default:
      model: gpt-4o
  mcpServers:
    - name: aws-mcp
      toolsPrefix: aws
```

Apply the Agent and test a read-only operation against a non-sensitive
resource. Confirm the IAM role denies an action outside its effective
permissions. A tool allowlist can reduce which tools the Agent sees, but AWS
IAM policies and applicable organization controls determine the permissions
for every signed request.

## Troubleshooting

- **Credential process fails:** Check that the mounted certificate matches the
  private key, the certificate chains to the trust anchor, the role trust
  policy permits the trust anchor and certificate identity, and the profile
  includes the role.
- **Access denied:** Check the role's permissions policy and the requested
  resource. Tool filtering does not change role permissions.
- **MCP server cannot connect:** Confirm the AWS MCP endpoint is available in
  the selected Region and that the pod can reach it over HTTPS.
- **Pod cannot start:** Check the image architecture, image pull access, and
  that the configured helper and entrypoint paths exist in the image.
