/* eslint-disable @typescript-eslint/no-explicit-any */
export type LabCtr = {
  id: string;
  name: string;
  service?: string;
  image: string;
  state: string;
  status: string;
  runtime?: string;
  ports: { PublicPort?: number; PrivatePort: number; Type: string; IP?: string }[];
  ips: string[];
  [k: string]: any;
};
export type LabVM = { name: string; state: string; accel?: string; lan?: string; ports: string[]; sshPort?: number; vnc?: boolean; autostart?: boolean; serial?: number; noCloudInit?: boolean; rejected?: number; noIdlePC?: boolean };
export type Lab = {
  id: string;
  name: string;
  loose?: boolean;
  workingDir?: string;
  files?: string[];
  running: number;
  total: number;
  runtimes?: string[];
  vms?: LabVM[];
  containers: LabCtr[];
};
