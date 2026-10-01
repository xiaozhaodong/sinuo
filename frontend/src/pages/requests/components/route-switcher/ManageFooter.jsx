import { ArrowRight } from 'lucide-react';

const ManageFooter = ({ description, label = '管理映射', onClick }) => (
  <div className="flex items-center gap-3 border-t border-line-soft px-4 py-2">
    <span className="min-w-0 flex-1 truncate text-[11px] text-fg-subtle">{description}</span>
    <button type="button" onClick={onClick} className="inline-flex shrink-0 items-center gap-1 text-[11px] font-medium text-accent hover:text-accent-fg">
      {label}<ArrowRight size={12} />
    </button>
  </div>
);

export default ManageFooter;
