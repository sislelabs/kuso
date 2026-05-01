// Slim kubernetes types. v0.1 IKubectlPipeline / IKubectlApp shapes are
// gone with the deleted modules.

export interface IStorageClass {
  name: string;
  provisioner: string;
  reclaimPolicy: string;
  volumeBindingMode: string;
}
