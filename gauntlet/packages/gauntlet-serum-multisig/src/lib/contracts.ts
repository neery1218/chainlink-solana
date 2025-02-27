import { contracts } from '@chainlink/gauntlet-solana'

export enum CONTRACT_LIST {
  MULTISIG = 'serum_multisig',
}

export const CONTRACT_ENV_NAMES = {
  [CONTRACT_LIST.MULTISIG]: 'PROGRAM_ID_MULTISIG',
}

export const { getContract, getDeploymentContract } = contracts.registerContracts(
  CONTRACT_LIST,
  CONTRACT_ENV_NAMES,
  'packages/gauntlet-serum-multisig/artifacts',
)
