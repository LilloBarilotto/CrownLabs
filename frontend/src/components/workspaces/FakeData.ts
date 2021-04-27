import { WorkspaceRole } from './../../utils';
const data = [
  {
    id: 0,
    title: 'Reti Locali e Data Center',
    role: 'manager' as WorkspaceRole,
    templates: [
      {
        id: '0_1',
        name: 'Ubuntu VM',
        gui: true,
        instances: [
          { id: 1, name: 'Ubuntu VM', ip: '192.168.0.1', status: true },
          { id: 2, name: 'Ubuntu VM', ip: '192.168.0.1', status: false },
        ],
      },
      { id: '0_2', name: 'Ubuntu VM', gui: false, instances: [] },
      {
        id: '0_3',
        name: 'Windows VM',
        gui: true,
        instances: [
          { id: 1, name: 'Windows VM', ip: '192.168.0.1', status: true },
        ],
      },
      { id: '0_4', name: 'Console (Linux)', gui: false, instances: [] },
      {
        id: '0_5',
        name: 'Ubuntu VM',
        gui: true,
        instances: [
          { id: 1, name: 'Ubuntu VM', ip: '192.168.0.1', status: true },
          { id: 2, name: 'Ubuntu VM', ip: '192.168.0.1', status: true },
          { id: 3, name: 'Ubuntu VM', ip: '192.168.0.1', status: true },
        ],
      },
      {
        id: '0_6',
        name: 'Ubuntu VM',
        gui: true,
        instances: [
          { id: 1, name: 'Ubuntu VM', ip: '192.168.0.1', status: true },
          { id: 2, name: 'Ubuntu VM', ip: '192.168.0.1', status: true },
          { id: 3, name: 'Ubuntu VM', ip: '192.168.0.1', status: true },
        ],
      },
    ],
  },
  {
    id: 1,
    title: 'Tecnologie e Servizi di Rete',
    role: 'user' as WorkspaceRole,
    templates: [
      {
        id: '1_1',
        name: 'Ubuntu VM',
        gui: true,
        instances: [
          { id: 1, name: 'Ubuntu VM', ip: '192.168.0.1', status: true },
          { id: 2, name: 'Ubuntu VM', ip: '192.168.0.1', status: true },
          { id: 3, name: 'Ubuntu VM', ip: '192.168.0.1', status: true },
        ],
      },
      { id: '1_2', name: 'Ubuntu VM', gui: false, instances: [] },
      { id: '1_3', name: 'Windows VM', gui: true, instances: [] },
    ],
  },
  {
    id: 2,
    title: 'Applicazioni Web I',
    role: 'user' as WorkspaceRole,
    templates: [
      { id: '2_1', name: 'Ubuntu VM', gui: true, instances: [] },
      { id: '2_2', name: 'Windows VM', gui: true, instances: [] },
      { id: '2_3', name: 'Console (Linux)', gui: false, instances: [] },
    ],
  },
  {
    id: 3,
    title: 'Cloud Computing',
    role: 'user' as WorkspaceRole,
    templates: [
      { id: '3_1', name: 'Console (Linux)', gui: false, instances: [] },
    ],
  },
  {
    id: 4,
    title: 'Programmazione di Sistema',
    role: 'user' as WorkspaceRole,
    templates: [],
  },
  {
    id: 5,
    title: 'Information System Security',
    role: 'user' as WorkspaceRole,
    templates: [],
  },
  {
    id: 6,
    title: 'Ingegneria del Software',
    role: 'user' as WorkspaceRole,
    templates: [],
  },
  {
    id: 7,
    title: 'Data Science',
    role: 'user' as WorkspaceRole,
    templates: [],
  },
  {
    id: 8,
    title: 'Software Networking',
    role: 'user' as WorkspaceRole,
    templates: [],
  },
];

export default data;
