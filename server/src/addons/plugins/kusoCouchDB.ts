import { Plugin, } from './plugin';
import { IPlugin, IPluginFormFields  } from './plugin.interface';

// Classname must be same as the CRD's Name
export class KusoCouchDB extends Plugin implements IPlugin {
  public id: string = 'kuso-operator'; //same as operator name
  public displayName = 'CouchDB';
  public icon = '/img/addons/couchdb.svg';
  public install: string = '';
  public url =
    'https://artifacthub.io/packages/olm/community-operators/kuso-operator';
  public docs = [
    {
      title: 'Kuso Docs',
      url: '',
    },
  ];
  public artifact_url =
    'https://artifacthub.io/api/v1/packages/olm/kuso/kuso-operator';
  public beta: boolean = false;

  public formfields: { [key: string]: IPluginFormFields } = {
    'KusoCouchDB.metadata.name': {
      type: 'text',
      label: 'Couchdb DB Name',
      name: 'metadata.name',
      required: true,
      default: 'couchdb',
      description: 'The name of the Couchdb instance',
    },
    'KusoCouchDB.spec.couchdb.image.tag': {
      type: 'combobox',
      label: 'Version/Tag',
      options: ['3.2.1', '3.3', '3.4.2', 'latest'], // TODO - load this dynamically
      name: 'spec.couchdb.image.tag',
      required: true,
      default: '3.2.1',
      description: 'Version of the PostgreSQL image to use',
    },
    'KusoCouchDB.spec.couchdb.clusterSize': {
      type: 'number',
      label: 'Cluster Size*',
      name: 'spec.couchdb.clusterSize',
      default: 3,
      required: true,
      description: 'Number of replicas',
    },
    'KusoCouchDB.spec.couchdb.persistentVolume.storageClass': {
      type: 'select-storageclass',
      label: 'Storage Class',
      // options: ['default', 'local-path', 'nfs-client', 'rook-ceph-block'],
      name: 'spec.couchdb.persistentVolume.storageClass',
      default: 'default',
      required: true,
    },
    'KusoCouchDB.spec.couchdb.persistentVolume.size': {
      type: 'text',
      label: 'Storage Size*',
      name: 'spec.couchdb.persistentVolume.size',
      default: '8Gi',
      required: true,
      description: 'Size of the storage',
    },
    'KusoCouchDB.spec.couchdb.adminUsername': {
      type: 'text',
      label: 'Admin Username*',
      name: 'spec.couchdb.adminUsername',
      default: 'admin',
      required: true,
      description: 'Admin Username',
    },
    'KusoCouchDB.spec.couchdb.adminPassword': {
      type: 'text',
      label: 'Admin Password*',
      name: 'spec.couchdb.auth.rootPassword',
      default: '',
      required: true,
      description: 'Admin Password',
    },
    'KusoCouchDB.spec.couchdb.adminHash': {
      type: 'text',
      label: 'Admin Hash*',
      name: 'spec.couchdb.adminHash',
      default: '',
      required: true,
      description: 'Random character string',
    },
    'KusoCouchDB.spec.couchdb.cookieAuthSecret': {
      type: 'text',
      label: 'Cookie Auth Secret*',
      name: 'spec.couchdb.cookieAuthSecret',
      default: '',
      required: true,
      description: 'Random character string',
    },
    'KusoCouchDB.spec.couchdb.couchdbConfig.couchdb.uuid': {
      type: 'text',
      label: 'instance UUID*',
      name: 'spec.couchdb.couchdbConfig.couchdb.uuid',
      default: '',
      required: true,
      description: 'Random character string',
    },
  };

  public env: any[] = [];

  protected additionalResourceDefinitions: object = {};

  constructor(availableOperators: any) {
    super();
    super.init(availableOperators);
  }
}
