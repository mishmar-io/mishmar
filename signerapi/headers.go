package signerapi

// InstanceHeader is on every answer of the service: the id of its run, which
// changes with a restart and takes the presigned nonces and FROST
// commitments with it. The engine folds it into the instance id it reports
// to the agents' signers, so they presign again before their next order.
const InstanceHeader = "Mishmar-Signer-Instance"
