---
key: ROX-32132
summary: "Init Container Vulnerability Scanning (Tech Preview)"
status: "Release Pending"
type: Feature
size: XL
priority: Major
assignee: "Vu Le"
reporter: "Doron Caspin"
fix_version: [4.11.0]
labels: [4.11.0, review]
components: [Vuln Management]
team: "ACS Core Workflows"
product_manager: "Boaz Michaely"
activity_type: "Product / Portfolio Work"
release_note: "RHACS now scans images used by init containers and displays init container info, if it exists, in the deployment details"
release_note_type: Enhancement
release_type: "Tech Preview"
product_docs: Yes
target_version: 4.11.0
resolution: Done
created: 2025-12-03
updated: 2026-05-28
resolved: 2026-05-19
---

# Init Container Vulnerability Scanning (Tech Preview)

### Goal Summary:

An elevator pitch (value statement) that describes the Feature in a clear, concise way. Complete during **New** status.

RHACS will include init container images in its image-scanning flow so that when a deployment or pipeline check references init containers, their images are scanned for vulnerabilities and configuration issues in the same way as main container images. This closes the gap where init container images were never scanned, so organizations have visibility and scan data for all container images in a deployment. Policy evaluation against init containers is out of scope for this phase (see ROX-33067).

---

### Background

**What are init containers?**
Init containers are part of the pod spec (`spec.initContainers`). They run at pod startup, before main containers, and must complete successfully before the main containers start. They use the same container spec (image, securityContext, resources, etc.) as main containers.

**Why scan init container images?**
Init container images carry the same image-level risks as main container images: CVEs, misconfigurations, and non-compliant or disallowed content. A vulnerable or malicious init container image can compromise the pod before the main workload runs. Kubernetes and Pod Security Standards treat init and main containers as separate but equivalent for security policy. Init container images should therefore be scanned for the same reasons as main container images.

**How does RHACS scan images today?**
RHACS scans images when there is a concrete need. Scanning is triggered in these situations:

- **Pipeline:**
  - `roxctl image scan` – explicit scan of a given image.
  - `roxctl image check` – evaluates build-time policies against a given image; a scan is triggered if one has not already been done.
  - `roxctl deployment check` – evaluates the deployment (including referenced images); scanning is triggered for images that have not yet been scanned.
- **Deployment attempt:**
When a user deploys a workload, RHACS discovers the images referenced in the deployment and ensures they are scanned .

**The gap**

Today, this flow considers only main container images (`spec.containers[*].image`). Images from `spec.initContainers[*].image` are not included, so init container images are never scanned and do not appear in Risk View, Vulnerability Management, or other deployment views.

---

### Goals and expected user outcomes:

The observable functionality that the user now has as a result of receiving this feature. Include the anticipated primary user type/persona and which existing features, if any, will be expanded. Complete during **New** status.

**Observable functionality:**

- In all situations where RHACS triggers image scanning, init container images that are in scope are included in the set of images that are scanned, in the same way as main container images.
- Scan results (vulnerabilities, configuration findings) for init container images are stored and displayed in the RHACS UI and API, and are attributable to the init container(s) and deployment (e.g. in Vulnerability Management, Risk View, Violation View, Network Graph view, and reporting).
- Init container image data is clearly identifiable as init-container-related in the UI/API, while remaining part of the overall deployment and cluster visibility.
- Init container info, if present, is added to the deployment information wherever it is available today (Risk View, Violation View, Network Graph view).

**Anticipated primary user types:**

- **Security / compliance:** Needs visibility into all container images in a deployment, including init containers, for risk and compliance.
- **Platform / DevOps:** Needs reliable scan data for every image that will run (including init containers) for risk assessment and deployment gating.

**Existing features expanded:**

- **Image scanning / vulnerability management:** Scanning scope and stored results are extended to include images referenced in `spec.initContainers[*].image` when those images are in scope (deployment or pipeline check).
- **Deployment info:** Risk View, Violation View, Network Graph view,  include  init container info.

**Out of scope :**

- Retroactive scanning for workloads that were deployed before this feature is enabled (focus is on new/updated deployments and pipeline checks).

---

### Acceptance Criteria:

A list of specific needs or objectives that a feature must deliver in order to be considered complete. Be sure to include nonfunctional requirements such as security, reliability, performance, maintainability, scalability, usability, etc. Initial completion during **Refinement** status.

**Functional**

- AC1: When a deployment is submitted or updated (or a deployment check is run), RHACS discovers images referenced in `spec.initContainers[*].image` and includes them in the set of images that are scanned, using the same on-demand logic as for `spec.containers[*].image`.
- AC2: Pipeline flows that trigger scanning (e.g. `roxctl image check`, `roxctl deployment check`) cause init container images referenced in the deployment or check input to be scanned when they would not otherwise have scan data, consistent with main container behavior.
- AC3: Scan results for init container images are stored and exposed via the RHACS API and UI. ~~Results are attributable to the init container(s) and the deployment (e.g. by container name/type and deployment)~~ at the image and deployment level (BM Feb 25 updated)
- A~~C4: Vulnerability Management, Risk View, Violation View, Network Graph view, and other deployment/risk views display init container image scan data in a way that is distinguishable from main container data but integrated into deployment and cluster risk (e.g. risk score reflects init container image findings)~~. (BM, Feb 25, this requirement has been deemed out of scope)
- AC5 Init container info, if present, is added to the deployment information wherever it is available today (Risk View, Violation View, Network Graph view).

**Non-functional**

- **Performance:** Including init container images in the scanning flow does not introduce unacceptable latency to deployment-time checks or the monitoring loop; any additional scan load is consistent with existing scanner behavior for additional images.
- **Reliability:** Scanner and related components reliably process init container images, including images from private registries, using the same integrations and credentials as for main container images.
- **Usability:** Init container scan data is clearly labeled as init-container-related in the UI/API (e.g. container type or role) so users can distinguish it from main container data without losing integration in deployment and cluster views.
- **Maintainability:** Changes are localized to components that discover deployment images and trigger or associate scans (e.g. sensor, scanner integration, deployment/risk aggregation), with minimal unnecessary impact on unrelated components.

---

### Design considerations

These are topics for the team to consider when specifying epic requirements and design

**1. API / data model**
Backwards compatibility: new fields or sub-objects (e.g. init container list, scan results for init container images), no removal or renaming of existing deployment/container fields. Clients that only read main containers continue to work. Document that deployment/image data may now include init container entries and how to distinguish them. Strict schema validation or hard-coded assumptions in consumers could break if the design is not additive.

**2. "Containers" vs "images" assumptions**
Today many flows might assume "deployment images" = "main container images" (1:1 or fixed structure). After this change, "deployment images" = main + init container images. Any logic that assumes "number of images = number of main containers" or "first N images are main only" can break (reports, dashboards, integrations, internal aggregation). Design so deployment/image APIs and views clearly distinguish container type (main vs init) and document that "images" can include both.

**3. UI / views**
Deployments with no init containers must look and behave as today. Deployments with init containers gain extra data; existing filters or "per-container" UX must still work and must not assume only main containers. 

**4. CLI / scripts**
`roxctl deployment check`, `roxctl image check`, or any text output that lists containers/images may change format or line count. Scripts that parse output (e.g. by line count or "one block per container") could break. Keep existing output structure for main containers; add init container lines in a predictable, labeled way (e.g. "Init container: ..." vs "Container: ...") and document the format so scripts can opt in or ignore.

**5. Migration (Storage / schema)**
New tables or columns for init container (meta)data and scan results should be additive. Old data (no init container info) should remain readable; Are we going to mandate a migration ?

---

### Success Criteria or KPIs measured:

A list of specific, measurable criteria that will be used to determine if the feature is successful. Include key performance indicators (KPIs) or other metrics. *Initial completion during ****Refinement**** status.* 

- Init container images referenced by deployments (or pipeline deployment checks) in scope are scanned within the same time/trigger expectations as main container images (e.g. within standard scan interval or on next deployment/pipeline trigger).
- Vulnerability Management, Risk View, and other deployment views show init container image results for deployments that use init containers; no regression in visibility or accuracy for main container image data.
- Deployment and cluster risk scores incorporate init container image findings where applicable, with no unintended regression in admission or reporting latency beyond acceptable baseline.

---

### Use Cases (Optional):

Include use case diagrams, main success scenarios, alternative flow scenarios together with user type/persona. Initial completion during **Refinement** status.

 

1.  

---

### Out of Scope (Optional):

High-level list of items that are out of scope. Initial completion during **Refinement** status.

- Policy support

## Status Summary

New design document is proposed for review and in the team queue
Plan is to deliver scanning only in 4.11 behind a feature flag, full feature in 5.0 on for all customers



See meetings notes for detail

