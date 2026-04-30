import { Plugin, } from './plugin';
import { IPlugin, IPluginFormFields  } from './plugin.interface';

// Classname must be same as the CRD's Name
export class KusoMail extends Plugin implements IPlugin {
  public id: string = 'kuso-operator'; //same as operator name
  public displayName = 'Haraka Mail Server';
  public icon = '/img/addons/Haraka.png';
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
    'KusoMail.metadata.name': {
      type: 'text',
      label: 'Mail Server Name',
      name: 'metadata.name',
      required: true,
      default: 'haraka',
      description: 'The name of the mail server instance',
    },
    'KusoMail.spec.haraka.haraka.env[0].value': {
      type: 'text',
      label: 'Hostlist*',
      name: 'KusoMail.spec.haraka.haraka.env[0].value',
      default: 'localhost,localhost.kuso.sislelabs.com',
      required: true,
      description:
        'A comma separated list of hostnames for which the mail server should accept mail',
    },
    'KusoMail.spec.haraka.haraka.env[1].value': {
      type: 'text',
      label: 'Server name*',
      name: 'KusoMail.spec.haraka.haraka.env[1].value',
      default: 'info',
      required: true,
      description: 'Single string for the server name: me',
    },
    'KusoMail.spec.haraka.haraka.env[6].value': {
      type: 'text',
      label: 'Log Level*',
      name: 'KusoMail.spec.haraka.haraka.env[6].value',
      default: 'info',
      required: true,
      description: 'HaraKa log level: info, warn, error, debug',
    },
  };

  public env: any[] = [];

  protected additionalResourceDefinitions: object = {};

  constructor(availableOperators: any) {
    super();
    super.init(availableOperators);
  }
}
