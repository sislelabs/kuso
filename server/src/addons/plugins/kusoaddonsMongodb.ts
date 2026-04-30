import { Plugin, } from './plugin';
import { IPlugin, IPluginFormFields  } from './plugin.interface';


// Classname must be same as the CRD's Name
export class KusoAddonMongodb extends Plugin implements IPlugin {
  public id: string = 'kuso-operator'; //same as operator name
  public displayName = 'MongoDB';
  public description: string =
    'MongoDB(R) is a relational open source NoSQL database. Easy to use, it stores data in JSON-like documents. Automated scalability and high-performance. Ideal for developing cloud native applications.';
  public icon = '/img/addons/mongo.svg';
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
  public deprecated: boolean = false;

  public formfields: { [key: string]: IPluginFormFields } = {
    'KusoAddonMongodb.metadata.name': {
      type: 'text',
      label: 'MongoDB Instance Name',
      name: 'metadata.name',
      required: true,
      default: 'mongodb',
      description: 'The name of the MongoDB instance',
    },
    'KusoAddonMongodb.spec.mongodb.image.tag': {
      type: 'combobox',
      label: 'Version/Tag',
      options: ['5', '6', '7', 'latest'], // TODO - load this dynamically
      name: 'spec.mongodb.image.tag',
      required: true,
      default: '7',
      description: 'Version of the MongoDB version to use',
    },
    'KusoAddonMongodb.spec.mongodb.settings.rootUsername': {
      type: 'text',
      label: 'MongoDB RootUser*',
      name: 'spec.mongodb.settings.rootUsername',
      default: 'mongodb',
      required: true,
      description: 'Username for the "mongodb" root user',
    },

    'KusoAddonMongodb.spec.mongodb.settings.rootPassword': {
      type: 'text',
      label: 'MongoDB Root Password*',
      name: 'spec.mongodb.settings.rootPassword',
      default: '',
      required: true,
      description: 'Password for the "mongodb" root user',
    },
    'KusoAddonMongodb.spec.mongodb.userDatabase.user': {
      type: 'text',
      label: 'Username*',
      name: 'spec.mongodb.userDatabase.user',
      default: '',
      required: true,
      description: 'Username for an additional user to create',
    },
    'KusoAddonMongodb.spec.mongodb.userDatabase.password': {
      type: 'text',
      label: 'User Password*',
      name: 'spec.mongodb.userDatabase.password',
      default: '',
      required: true,
      description: 'Password for an additional user to create',
    },
    'KusoAddonMongodb.spec.mongodb.userDatabase.name': {
      type: 'text',
      label: 'Database*',
      name: 'spec.mongodb.userDatabase.name',
      default: 'mymongodb',
      required: true,
      description: 'Name for a custom database to create',
    },
    'KusoAddonMongodb.spec.mongodb.replicaSet.enabled': {
      type: 'switch',
      label: 'Enable Replica Set',
      name: 'spec.mongodb.replicaSet.enabled',
      default: false,
      required: false,
      description: 'Enable MongoDB replica set',
    },
    'KusoAddonMongodb.spec.mongodb.replicaSet.secondaries': {
      type: 'number',
      label: 'Replica secondaries',
      name: 'spec.mongodb.replicaSet.secondaries',
      default: 2,
      required: false,
      description: 'Number of MongoDB replica set secondaries',
    },
    'KusoAddonMongodb.spec.mongodb.storage.className': {
      type: 'select-storageclass',
      label: 'Storage Class',
      // options: ['default', 'local-path', 'nfs-client', 'rook-ceph-block'],
      name: 'spec.mongodb.storage.className',
      default: 'default',
      required: true,
    },
    'KusoAddonMongodb.spec.mongodb.storage.requestedSize': {
      type: 'text',
      label: 'Storage Size*',
      name: 'spec.mongodb.storage.requestedSize',
      default: '1Gi',
      required: true,
      description: 'Size of the storage',
    },
  };

  public env: any[] = [];

  public resourceDefinitions: object = {
    KusoAddonMongodb: {
      apiVersion: "application.kuso.sislelabs.com/v1alpha1",
      kind: "KusoAddonMongodb",
      metadata: {
        name: "mongodb"
      },
      spec: {
        mongodb: {
          image: {
            tag: ""
          },
          resources: {},
          replicaSet: {
            enabled: false,
            name: "repl",
            clusterDomain: "cluster.local",
            secondaries: 2,
            hiddenSecondaries: {
              instances: 0,
              volumeName: "mongodb-hidden-volume"
            }
          },
          settings: {
            rootUsername: null,
            rootPassword: null
          },
          userDatabase: {
            name: "kuso_database",
            user: "kuso_user",
            password: "kuso_password"
          },
          storage: {
            volumeName: "mongodb-volume",
            requestedSize: null,
            className: null,
            accessModes: [
              "ReadWriteOnce"
            ],
            keepPvc: false
          }
        }
      }
    }
  }

  protected additionalResourceDefinitions: object = {};

  constructor(availableOperators: any) {
    super();
    super.init(availableOperators);
  }
}
