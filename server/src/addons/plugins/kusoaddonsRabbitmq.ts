import { Plugin, } from './plugin';
import { IPlugin, IPluginFormFields  } from './plugin.interface';


// Classname must be same as the CRD's Name
export class KusoAddonRabbitmq extends Plugin implements IPlugin {
  public id: string = 'kuso-operator'; //same as operator name
  public displayName = 'RabbitMQ';
  public description = 'RabbitMQ is an open source general-purpose message broker that is designed for consistent, highly-available messaging scenarios (both synchronous and asynchronous).';
  public icon = '/img/addons/rabbitmq.svg';
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
    'KusoAddonRabbitmq.metadata.name': {
      type: 'text',
      label: 'RabbitMQ Instance Name',
      name: 'metadata.name',
      required: true,
      default: 'rabbitmq',
      description: 'The name of the RabbitMQ instance',
    },
    'KusoAddonRabbitmq.spec.rabbitmq.image.tag': {
      type: 'combobox',
      label: 'Version/Tag',
      options: ['3', '4', 'latest'], // TODO - load this dynamically
      name: 'spec.rabbitmq.image.tag',
      required: true,
      default: '4',
      description: 'Version of the RabbitMQ version to use',
    },
    'KusoAddonRabbitmq.spec.rabbitmq.authentication.erlangCookie.value': {
      type: 'text',
      label: 'RabbitMQ Erlang Cookie*',
      name: 'spec.rabbitmq.authentication.erlangCookie.value',
      default: '',
      required: true,
      description: 'Erlang cookie name for RabbitMQ',
    },
    'KusoAddonRabbitmq.spec.rabbitmq.authentication.user.value': {
      type: 'text',
      label: 'Username*',
      name: 'spec.rabbitmq.authentication.user.value',
      default: '',
      required: true,
      description: 'Username for rabbitmq user to create',
    },
    'KusoAddonRabbitmq.spec.rabbitmq.authentication.password.value': {
      type: 'text',
      label: 'User Password*',
      name: 'spec.rabbitmq.authentication.password.value',
      default: '',
      required: true,
      description: 'Password for rabbitmq user to create',
    },
    'KusoAddonRabbitmq.spec.rabbitmq.storage.className': {
      type: 'select-storageclass',
      label: 'Storage Class',
      // options: ['default', 'local-path', 'nfs-client', 'rook-ceph-block'],
      name: 'spec.rabbitmq.storage.className',
      default: 'default',
      required: true,
    },
    'KusoAddonRabbitmq.spec.rabbitmq.storage.requestedSize': {
      type: 'text',
      label: 'Storage Size*',
      name: 'spec.rabbitmq.storage.requestedSize',
      default: '1Gi',
      required: true,
      description: 'Size of the storage',
    },
  };

  public env: any[] = [];

  public resourceDefinitions: object = {
    KusoAddonRabbitmq: {
      apiVersion: "application.kuso.sislelabs.com/v1alpha1",
      kind: "KusoAddonRabbitmq",
      metadata: {
        name: "rabbitmq"
      },
      spec: {
        rabbitmq: {
          image: {
            tag: ""
          },
          replicaCount: 1,
          serviceMonitor: {
            enabled: false
          },
          revisionHistoryLimit: null,
          clusterDomain: "cluster.local",
          plugins: [],
          authentication: {
            user: {
              value: "kuso_user"
            },
            password: {
              value: "kuso_password"
            },
            erlangCookie: {
              value: "kuso_erlang_cookie"
            }
          },
          options: {
            memoryHighWatermark: {
              enabled: false,
              type: "relative",
              value: 0.4,
              pagingRatio: null
            },
            memory: {
              totalAvailableOverrideValue: null,
              calculationStrategy: null
            }
          },
          managementPlugin: {
            enabled: false
          },
          prometheusPlugin: {
            enabled: true
          },
          storage: {
            volumeName: "rabbitmq-volume",
            requestedSize: null,
            className: null,
            accessModes: [
              "ReadWriteOnce"
            ]
          }
        }
      }
    }
  };

  protected additionalResourceDefinitions: object = {};

  constructor(availableOperators: any) {
    super();
    super.init(availableOperators);
  }
}
